package main

import (
	"bytes"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// elem is an open element on the scan stack.
type elem struct {
	name  string
	attrs string
	group int // group id if this element is a grouping element, else -1
}

// textNode is the inner text span of a target element.
type textNode struct {
	start, end       int // content byte range in data
	tagStart, tagEnd int // open tag byte range (tagEnd = index after '>')
	group            int
	name             string
	text             string // decoded
	blank            bool   // not part of group text; emptied when group changes (ruby readings)
}

// spanRem is a byte range to delete when its group changed (e.g. furigana rPh).
type spanRem struct {
	start, end int
	group      int
}

// partRule decides how a given XML part is scanned.
type partRule struct {
	// isTarget: whether an element (with the current stack) holds text to process.
	isTarget func(name, attrs string, stack []elem) bool
	// isGroup: whether an element groups its descendant texts into one logical string.
	isGroup func(name string) bool
	// isRemovableInChangedGroup: elements to delete when their group text changed.
	isRemovable func(name string) bool
	// preserveSpace: add xml:space="preserve" to changed target tags.
	preserveSpace func(name string) bool
	// isBlankCtx: target texts in this context are emptied when their group changes.
	isBlankCtx func(stack []elem) bool
	// keepEntities: 書き換えたテキストを、元と同じ文字参照（&quot; &#10; など）で書き戻す要素（ヘッダー・フッター。改善5）。
	// 元に戻したときに、元のヘッダーとバイト単位で同じになるようにする
	keepEntities func(name string) bool
}

// groupResult is what a transform returns for one group's text.
type match struct {
	start, end int // byte offsets in group text
	repl       string
	kind       string
	orig       string
}

// transformFunc: hdr はヘッダー・フッターの中の文字（改善5：年齢も置き換える）
type transformFunc func(groupText string, suspectOK bool, hdr bool) []match

// groupTF: name はそのグループの最初の要素の名前（oddHeader など）
type groupTF func(groupText string, group int, name string) []match

type groupReport struct {
	before  string
	after   string
	matches []match
}

func decodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '&' {
			j := strings.IndexByte(s[i:], ';')
			if j > 0 && j < 12 {
				ent := s[i+1 : i+j]
				rep := ""
				ok := true
				switch ent {
				case "amp":
					rep = "&"
				case "lt":
					rep = "<"
				case "gt":
					rep = ">"
				case "quot":
					rep = "\""
				case "apos":
					rep = "'"
				default:
					if strings.HasPrefix(ent, "#x") || strings.HasPrefix(ent, "#X") {
						n, err := strconv.ParseInt(ent[2:], 16, 32)
						if err == nil {
							rep = string(rune(n))
						} else {
							ok = false
						}
					} else if strings.HasPrefix(ent, "#") {
						n, err := strconv.ParseInt(ent[1:], 10, 32)
						if err == nil {
							rep = string(rune(n))
						} else {
							ok = false
						}
					} else {
						ok = false
					}
				}
				if ok {
					b.WriteString(rep)
					i += j + 1
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func encodeText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

var reCharRef = regexp.MustCompile(`&(?:quot|apos|#[0-9]+|#[xX][0-9a-fA-F]+);`)

// needsPreserve: xml:space="preserve" がないと空白が消えるおそれのある文字か（前後の空白・タブ・改行・続いた空白）
func needsPreserve(t string) bool {
	if t == "" {
		return false
	}
	if strings.TrimSpace(t) != t {
		return true
	}
	return strings.Contains(t, "  ") || strings.ContainsAny(t, "\t\n\r")
}

// encodeLike: encodeText のあと、元の文字列（raw）で文字参照として書かれていた字（" ' 改行 など）を、同じ参照で書く。
// & < > は encodeText のとおり。ヘッダー・フッターを元に戻したときにバイト単位で同じにするため（改善5）
func encodeLike(raw, s string) string {
	out := encodeText(s)
	if !strings.Contains(raw, "&") {
		return out
	}
	seen := map[string]bool{}
	for _, ref := range reCharRef.FindAllString(raw, -1) {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		ch := decodeEntities(ref)
		if ch == ref || ch == "&" || ch == "<" || ch == ">" || ch == "" {
			continue
		}
		out = strings.ReplaceAll(out, ch, ref)
	}
	return out
}

func findTagEnd(data []byte, i int) int {
	// i points at '<'. return index of the closing '>' (respecting quotes)
	var q byte
	for k := i + 1; k < len(data); k++ {
		c := data[k]
		if q != 0 {
			if c == q {
				q = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			q = c
			continue
		}
		if c == '>' {
			return k
		}
	}
	return -1
}

// scanXML walks the XML and returns target text nodes and removable spans.
func scanXML(data []byte, rule partRule) ([]textNode, []spanRem, bool) {
	var nodes []textNode
	var rems []spanRem
	var stack []elem
	nextGroup := 0
	curGroup := func() int {
		for k := len(stack) - 1; k >= 0; k-- {
			if stack[k].group >= 0 {
				return stack[k].group
			}
		}
		return -1
	}
	type openRem struct {
		start int
		depth int
		group int
	}
	var remOpen []openRem
	i := 0
	for i < len(data) {
		lt := bytes.IndexByte(data[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		if bytes.HasPrefix(data[i:], []byte("<?")) {
			e := bytes.Index(data[i:], []byte("?>"))
			if e < 0 {
				return nil, nil, false
			}
			i += e + 2
			continue
		}
		if bytes.HasPrefix(data[i:], []byte("<!--")) {
			e := bytes.Index(data[i:], []byte("-->"))
			if e < 0 {
				return nil, nil, false
			}
			i += e + 3
			continue
		}
		if bytes.HasPrefix(data[i:], []byte("<![CDATA[")) {
			e := bytes.Index(data[i:], []byte("]]>"))
			if e < 0 {
				return nil, nil, false
			}
			i += e + 3
			continue
		}
		if bytes.HasPrefix(data[i:], []byte("<!")) {
			e := bytes.IndexByte(data[i:], '>')
			if e < 0 {
				return nil, nil, false
			}
			i += e + 1
			continue
		}
		gt := findTagEnd(data, i)
		if gt < 0 {
			return nil, nil, false
		}
		inner := string(data[i+1 : gt])
		closing := strings.HasPrefix(inner, "/")
		selfClose := strings.HasSuffix(inner, "/")
		if closing {
			inner = inner[1:]
		}
		if selfClose {
			inner = inner[:len(inner)-1]
		}
		name := inner
		attrs := ""
		if k := strings.IndexAny(inner, " \t\r\n"); k >= 0 {
			name = inner[:k]
			attrs = inner[k:]
		}
		if closing {
			// pop to matching name
			for len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if len(remOpen) > 0 && remOpen[len(remOpen)-1].depth == len(stack) {
					r := remOpen[len(remOpen)-1]
					remOpen = remOpen[:len(remOpen)-1]
					rems = append(rems, spanRem{start: r.start, end: gt + 1, group: r.group})
				}
				if top.name == name {
					break
				}
			}
			i = gt + 1
			continue
		}
		if selfClose {
			i = gt + 1
			continue
		}
		// open tag
		if rule.isTarget != nil && rule.isTarget(name, attrs, stack) {
			// content until next '<'
			nlt := bytes.IndexByte(data[gt+1:], '<')
			if nlt < 0 {
				return nil, nil, false
			}
			cs, ce := gt+1, gt+1+nlt
			g := curGroup()
			if g < 0 {
				g = nextGroup
				nextGroup++
			}
			blank := rule.isBlankCtx != nil && rule.isBlankCtx(stack)
			nodes = append(nodes, textNode{start: cs, end: ce, tagStart: i, tagEnd: gt + 1, group: g, name: name, text: decodeEntities(string(data[cs:ce])), blank: blank})
			stack = append(stack, elem{name: name, attrs: attrs, group: -1})
			i = ce
			continue
		}
		g := -1
		if rule.isGroup != nil && rule.isGroup(name) {
			g = nextGroup
			nextGroup++
		}
		if rule.isRemovable != nil && rule.isRemovable(name) {
			remOpen = append(remOpen, openRem{start: i, depth: len(stack), group: curGroup()})
		}
		stack = append(stack, elem{name: name, attrs: attrs, group: g})
		i = gt + 1
	}
	return nodes, rems, true
}

type edit struct {
	start, end int
	repl       []byte
}

// processXML applies transform to every text group in the part.
func processXML(data []byte, rule partRule, tf groupTF) ([]byte, []groupReport, bool) {
	nodes, rems, ok := scanXML(data, rule)
	if !ok {
		return data, nil, false
	}
	// order groups by first appearance
	groupOrder := []int{}
	byGroup := map[int][]int{}
	blanks := map[int][]int{}
	for idx, n := range nodes {
		if n.blank {
			blanks[n.group] = append(blanks[n.group], idx)
			continue
		}
		if _, seen := byGroup[n.group]; !seen {
			groupOrder = append(groupOrder, n.group)
		}
		byGroup[n.group] = append(byGroup[n.group], idx)
	}
	var edits []edit
	var reports []groupReport
	changedGroups := map[int]bool{}
	curPartNo++
	for gi, g := range groupOrder {
		curGroupSeq = gi
		idxs := byGroup[g]
		var sb strings.Builder
		offs := make([]int, len(idxs)+1)
		for k, ni := range idxs {
			offs[k] = sb.Len()
			sb.WriteString(nodes[ni].text)
		}
		offs[len(idxs)] = sb.Len()
		full := sb.String()
		// 要素ごとの境目（住所のかたまりを境目で分けて置き換えるため。改善6）
		curGroupText, curGroupBounds = full, offs[1:len(idxs)]
		ms := tf(full, g, nodes[idxs[0]].name)
		curGroupText, curGroupBounds = "", nil
		if len(ms) == 0 {
			continue
		}
		sort.Slice(ms, func(a, b int) bool { return ms[a].start < ms[b].start })
		owner := func(pos int) int {
			for k := 0; k < len(idxs); k++ {
				if pos >= offs[k] && pos < offs[k+1] {
					return k
				}
			}
			return len(idxs) - 1
		}
		outs := make([]strings.Builder, len(idxs))
		mi := 0
		for pos := 0; pos < len(full); {
			if mi < len(ms) && ms[mi].start == pos {
				outs[owner(pos)].WriteString(ms[mi].repl)
				pos = ms[mi].end
				mi++
				continue
			}
			// copy one UTF-8 char
			size := 1
			for size < 4 && pos+size < len(full) && (full[pos+size]&0xC0) == 0x80 {
				size++
			}
			outs[owner(pos)].WriteString(full[pos : pos+size])
			pos += size
		}
		var after strings.Builder
		for k, ni := range idxs {
			nt := outs[k].String()
			after.WriteString(nt)
			if nt == nodes[ni].text {
				continue
			}
			n := nodes[ni]
			keep := rule.keepEntities != nil && rule.keepEntities(n.name)
			// ヘッダー・フッターは、空白を守る印が要るとき（前後の空白など）だけ付ける（元に戻したときにバイト単位で同じにするため。改善5）。
			// 元の文字にもともと改行などがあって印がなかったときは付けない（改善6：住所の改行を記号の外に残すため）
			if rule.preserveSpace != nil && rule.preserveSpace(n.name) && !strings.Contains(string(data[n.tagStart:n.tagEnd]), "xml:space") && (!keep || (needsPreserve(nt) && !needsPreserve(n.text))) {
				tag := string(data[n.tagStart : n.tagEnd-1])
				edits = append(edits, edit{start: n.tagStart, end: n.tagEnd, repl: []byte(tag + ` xml:space="preserve">`)})
			}
			enc := encodeText(nt)
			if keep {
				enc = encodeLike(string(data[n.start:n.end]), nt)
			}
			edits = append(edits, edit{start: n.start, end: n.end, repl: []byte(enc)})
		}
		changedGroups[g] = true
		for _, bi := range blanks[g] {
			if nodes[bi].start < nodes[bi].end {
				edits = append(edits, edit{start: nodes[bi].start, end: nodes[bi].end, repl: nil})
			}
		}
		reports = append(reports, groupReport{before: full, after: after.String(), matches: ms})
	}
	for _, r := range rems {
		if changedGroups[r.group] {
			edits = append(edits, edit{start: r.start, end: r.end, repl: nil})
		}
	}
	if len(edits) == 0 {
		return data, nil, true
	}
	sort.Slice(edits, func(a, b int) bool { return edits[a].start < edits[b].start })
	var out bytes.Buffer
	pos := 0
	for _, e := range edits {
		if e.start < pos { // overlapping (e.g. text inside removed span) – skip
			continue
		}
		out.Write(data[pos:e.start])
		out.Write(e.repl)
		pos = e.end
	}
	out.Write(data[pos:])
	return out.Bytes(), reports, true
}
