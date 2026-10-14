// AI作業パネルを課題用フォルダ（kadai_masking・kadai_ai・kadai_server）で自動操作し、その様子を動画（mp4）に撮る。
//
//   cd demo
//   npm install
//   node record.mjs --reset      … 課題用フォルダの中身を demo\_退避\日時\ へ移してから撮る（消さない）
//
// そのほかの指定：--show（撮っている画面を出す） --fast（待ち時間を短くする。試し用）
// パネルは run_source.bat をそのまま使って起動する（ブラウザを開かない・アドレスを受け取る、の2つだけ足す）。
// 撮る前に「マスキング作業フォルダ」の画面で、場所が kadai_panel\kadai_masking であることを確かめ、違えば止める。

import { spawn, execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";
import ffmpegPath from "ffmpeg-static";
import JSZip from "jszip";

const DEMO = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(DEMO, "..");
const SAMPLES = path.join(ROOT, "samples");
const KADAI = ["kadai_masking", "kadai_ai", "kadai_server"].map((d) => path.join(ROOT, d));
const VIDEOS = path.join(DEMO, "videos");
const args = new Set(process.argv.slice(2));
const FAST = args.has("--fast");
const W = 1280, H = 800;

const sleep = (ms) => new Promise((r) => setTimeout(r, FAST ? Math.min(ms, 150) : ms));
const stamp = () => {
  const d = new Date(), p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}_${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
};
const log = (...a) => console.log("・", ...a);

// ---------------- 課題用フォルダの準備 ----------------

// .gitkeep（フォルダの形）は残し、それ以外を退避先へ移す。.gitkeep のあるフォルダは中へ入って続ける
function moveAside(dir, dest) {
  let n = 0;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.name === ".gitkeep") continue;
    const src = path.join(dir, e.name), to = path.join(dest, e.name);
    if (e.isDirectory() && hasGitkeep(src)) {
      n += moveAside(src, to);
      continue;
    }
    fs.mkdirSync(dest, { recursive: true });
    fs.renameSync(src, to);
    n++;
  }
  return n;
}
function hasGitkeep(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.name === ".gitkeep") return true;
    if (e.isDirectory() && hasGitkeep(path.join(dir, e.name))) return true;
  }
  return false;
}
function leftovers(dir) {
  const out = [];
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.name === ".gitkeep") continue;
    const p = path.join(dir, e.name);
    if (e.isDirectory()) out.push(...leftovers(p));
    else out.push(p);
  }
  return out;
}

function prepareFolders() {
  if (args.has("--reset")) {
    const dest = path.join(DEMO, "_退避", stamp());
    let n = 0;
    for (const k of KADAI) n += moveAside(k, path.join(dest, path.basename(k)));
    log(n ? `課題用フォルダの中身 ${n} 件を ${path.relative(ROOT, dest)} へ移しました（消していません）` : "課題用フォルダは空でした");
  }
  const left = KADAI.flatMap(leftovers);
  if (left.length) {
    console.error("課題用フォルダに前の中身が残っています。--reset を付けて、退避してから撮ってください：");
    for (const f of left.slice(0, 10)) console.error("  " + path.relative(ROOT, f));
    process.exit(1);
  }
}

// ---------------- パネルの起動（run_source.bat） ----------------

async function startPanel() {
  fs.mkdirSync(VIDEOS, { recursive: true });
  const urlFile = path.join(VIDEOS, ".panel_url.txt");
  fs.rmSync(urlFile, { force: true });
  const env = { ...process.env, MASKTOOL_NOPAUSE: "1", MASKTOOL_PANEL_URLFILE: urlFile };
  const proc = spawn("cmd.exe", ["/d", "/c", path.join(ROOT, "run_source.bat")], { cwd: ROOT, env, windowsHide: true });
  let out = "";
  proc.stdout.on("data", (d) => (out += d));
  proc.stderr.on("data", (d) => (out += d));
  for (let i = 0; i < 240; i++) {
    if (fs.existsSync(urlFile) && fs.readFileSync(urlFile, "utf8").startsWith("http")) {
      return { proc, url: fs.readFileSync(urlFile, "utf8").trim() };
    }
    if (proc.exitCode !== null) break;
    await new Promise((r) => setTimeout(r, 500));
  }
  stopPanel(proc);
  throw new Error("パネルが起動しませんでした：\n" + out);
}
function stopPanel(proc) {
  if (proc && proc.exitCode === null) {
    try { execFileSync("taskkill", ["/PID", String(proc.pid), "/T", "/F"], { stdio: "ignore" }); } catch {}
  }
}

// ---------------- 画面の見せ方（字幕・マウスの印） ----------------

const OVERLAY = () => {
  if (document.getElementById("demo-cap")) return;
  const st = document.createElement("style");
  st.textContent = `#demo-cap{position:fixed;left:50%;bottom:22px;transform:translateX(-50%);z-index:99999;max-width:86%;
    background:rgba(20,30,45,.88);color:#fff;font:600 22px/1.5 "Yu Gothic UI","Meiryo",sans-serif;padding:12px 26px;border-radius:12px;
    box-shadow:0 4px 18px rgba(0,0,0,.35);text-align:center;pointer-events:none;transition:opacity .3s}
    #demo-cap small{display:block;font-weight:400;font-size:16px;opacity:.85}
    #demo-cur{position:fixed;z-index:100000;width:22px;height:22px;margin:-11px 0 0 -11px;border-radius:50%;
    background:rgba(255,140,0,.55);border:2px solid #e06c00;pointer-events:none;transition:transform .12s}
    #demo-cur.down{transform:scale(.7)}`;
  document.head.appendChild(st);
  const cap = document.createElement("div"); cap.id = "demo-cap"; cap.style.opacity = "0"; document.body.appendChild(cap);
  const cur = document.createElement("div"); cur.id = "demo-cur"; cur.style.left = "-50px"; document.body.appendChild(cur);
  addEventListener("mousemove", (e) => { cur.style.left = e.clientX + "px"; cur.style.top = e.clientY + "px"; }, true);
  addEventListener("dragover", (e) => { cur.style.left = e.clientX + "px"; cur.style.top = e.clientY + "px"; }, true);
  addEventListener("mousedown", () => cur.classList.add("down"), true);
  addEventListener("mouseup", () => cur.classList.remove("down"), true);
};

async function caption(page, title, sub = "", ms = 2200) {
  await page.evaluate(OVERLAY);
  await page.evaluate(([t, s]) => {
    const c = document.getElementById("demo-cap");
    c.innerHTML = "";
    c.append(t);
    if (s) { const sm = document.createElement("small"); sm.textContent = s; c.append(sm); }
    c.style.opacity = "1";
  }, [title, sub]);
  await sleep(ms);
}

let mouse = { x: W / 2, y: H / 2 };
async function moveTo(page, loc) {
  await loc.scrollIntoViewIfNeeded();
  await sleep(250);
  const b = await loc.boundingBox();
  if (!b) throw new Error("画面に見えない部品です");
  const x = b.x + b.width / 2, y = b.y + b.height / 2;
  await page.mouse.move(x, y, { steps: FAST ? 2 : 18 });
  mouse = { x, y };
  await sleep(350);
}
async function click(page, loc, after = 900) {
  await moveTo(page, loc);
  await loc.click();
  await sleep(after);
}
async function type(page, loc, text) {
  await click(page, loc, 200);
  await loc.fill("");
  await loc.pressSequentially(text, { delay: FAST ? 0 : 70 });
  await sleep(300);
}
async function waitIdle(page) {
  await page.waitForFunction(() => !document.querySelector("#busy.on"), null, { timeout: 120000 });
  await sleep(500);
}
async function goTop(page) {
  await page.evaluate(() => (location.hash = ""));
  await waitIdle(page);
}

// ---------------- AI役：30 の書類を 40 に置く ----------------

const AI_LINE = "R8.10.14（火）17:00【ケアプラン作成】【利用者001】様の夜間の排泄について、【家族001の名】さんの負担を減らす方法を次回の担当者会議で検討する。";

// 和暦（令和）の日付：R8.10.14 の形
const wareki = (d = new Date()) => `R${d.getFullYear() - 2018}.${d.getMonth() + 1}.${d.getDate()}`;
const CAREPLAN = `ケアプラン_第2表（${wareki()}作成）.xlsx`;

// AI役が作るケアプラン（第2表）。記号のまま書く。シート名「2表」、利用者名は E3（パネルの登録セルと同じ位置）
async function makeCarePlan(out) {
  const day = wareki(), d = new Date();
  const lt = `${day}〜${wareki(new Date(d.getFullYear(), d.getMonth() + 6, 0))}`; // 長期：6か月後の月末まで
  const st = `${day}〜${wareki(new Date(d.getFullYear(), d.getMonth() + 3, 0))}`; // 短期：3か月後の月末まで
  const needs = [
    ["夜中に2〜3回トイレに起き、本人も【家族001の名】さんも眠れていない。", "夜間もよく眠れ、日中を元気に過ごせる。", "夜間の排泄の回数が減り、【家族001の名】さんの睡眠が確保できる。", [
      ["夜間頻尿について主治医に相談する（受診に【家族002の名】さんが同行）", "", "受診・家族支援", "星降クリニック・長男", "月1回"],
      ["服薬確認、体調・排泄の状況の観察", "○", "訪問看護", "ほしぞら訪問看護ステーション", "週1回"],
      ["寝室からトイレまでの足元の明かりを整える", "", "家族支援", "妻・長男", "随時"]]],
    ["浴槽をまたぐときにふらつきがあり、転倒が心配。", "安全に入浴を続けられる。", "手すりを使って、浴槽の出入りが一人でできる。", [
      ["浴室に手すりを追加する", "○", "住宅改修", "（見積もり中）", "1回"],
      ["入浴時の見守り・介助", "○", "訪問介護", "ほしぞら訪問介護", "週2回"],
      ["下肢の筋力訓練", "○", "訪問看護", "ほしぞら訪問看護ステーション", "週1回"]]],
  ];
  const x = (t) => String(t).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const rows = {}, merges = [];
  const put = (ref, v, s) => { const r = +ref.match(/\d+/)[0]; (rows[r] = rows[r] || []).push(`<c r="${ref}" s="${s}" t="inlineStr"><is><t xml:space="preserve">${x(v)}</t></is></c>`); };
  const box = (ref, s = 1) => { const r = +ref.match(/\d+/)[0]; (rows[r] = rows[r] || []).push(`<c r="${ref}" s="${s}"/>`); };
  put("A1", "第2表", 4);
  put("A2", "居宅サービス計画書（2）", 3); merges.push("A2:K2");
  put("A3", "利用者名", 2); put("E3", "【利用者001】 殿", 1); for (const c of "BCD") box(c + "3", 2); for (const c of "FG") box(c + "3", 1);
  merges.push("A3:D3", "E3:G3");
  put("I3", "作成年月日", 2); put("J3", day, 1); box("K3"); merges.push("J3:K3");
  put("A5", "生活全般の解決すべき課題（ニーズ）", 2); box("A6", 2); merges.push("A5:A6");
  put("B5", "目標", 2); for (const c of "CDE") box(c + "5", 2); merges.push("B5:E5");
  put("F5", "援助内容", 2); for (const c of "GHIJK") box(c + "5", 2); merges.push("F5:K5");
  ["長期目標", "（期間）", "短期目標", "（期間）", "サービス内容", "※1", "サービス種別", "※2", "頻度", "期間"].forEach((h, i) => put("BCDEFGHIJK"[i] + "6", h, 2));
  let r = 7;
  for (const [need, l, s, svcs] of needs) {
    put("A" + r, need, 1); put("B" + r, l, 1); put("C" + r, lt, 1); put("D" + r, s, 1); put("E" + r, st, 1);
    for (const c of "ABCDE") { for (let k = 1; k < svcs.length; k++) box(c + (r + k)); merges.push(`${c}${r}:${c}${r + svcs.length - 1}`); }
    svcs.forEach(([sv, m, kind, who, fq], k) => { put("F" + (r + k), sv, 1); put("G" + (r + k), m, 5); put("H" + (r + k), kind, 1); put("I" + (r + k), who, 1); put("J" + (r + k), fq, 5); put("K" + (r + k), st, 1); });
    r += svcs.length;
  }
  put("A" + (r + 1), "※1「保険給付の対象かどうかの区分」について、保険給付対象内サービスについては○印を付す。", 4);
  put("A" + (r + 2), "※2「当該サービス提供を行う事業所」について記入する。", 4);
  const col = (c) => "ABCDEFGHIJK".indexOf(c[0]);
  const sheetRows = Object.keys(rows).map(Number).sort((a, b) => a - b).map((n) => {
    const ht = n >= 7 && n < r ? ' ht="54" customHeight="1"' : n === 2 ? ' ht="30" customHeight="1"' : n === 5 || n === 6 ? ' ht="30" customHeight="1"' : "";
    return `<row r="${n}"${ht}>${rows[n].sort((a, b) => col(a.match(/r="(\w)/)[1]) - col(b.match(/r="(\w)/)[1])).join("")}</row>`;
  }).join("");
  const widths = [30, 24, 13, 24, 13, 34, 5, 14, 24, 8, 13];
  const sheet = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheetPr><pageSetUpPr fitToPage="1"/></sheetPr><sheetViews><sheetView workbookViewId="0" showGridLines="0"/></sheetViews><cols>${widths.map((w, i) => `<col min="${i + 1}" max="${i + 1}" width="${w}" customWidth="1"/>`).join("")}</cols><sheetData>${sheetRows}</sheetData><mergeCells count="${merges.length}">${merges.map((m) => `<mergeCell ref="${m}"/>`).join("")}</mergeCells><pageMargins left="0.4" right="0.4" top="0.5" bottom="0.5" header="0.3" footer="0.3"/><pageSetup paperSize="9" orientation="landscape" fitToWidth="1" fitToHeight="1"/></worksheet>`;
  const font = (b, sz) => `<font>${b ? "<b/>" : ""}<sz val="${sz}"/><name val="游ゴシック"/><family val="3"/><charset val="128"/></font>`;
  const brd = '<border><left style="thin"/><right style="thin"/><top style="thin"/><bottom style="thin"/><diagonal/></border>';
  const styles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="3">${font(0, 11)}${font(1, 11)}${font(1, 16)}</fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFEEF2F6"/><bgColor indexed="64"/></patternFill></fill></fills><borders count="2"><border><left/><right/><top/><bottom/><diagonal/></border>${brd}</borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="6"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf><xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="center" vertical="center" wrapText="1"/></xf><xf numFmtId="0" fontId="2" fillId="0" borderId="0" xfId="0" applyFont="1" applyAlignment="1"><alignment horizontal="center" vertical="center"/></xf><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1" applyAlignment="1"><alignment horizontal="center" vertical="top" wrapText="1"/></xf></cellXfs><cellStyles count="1"><cellStyle name="標準" xfId="0" builtinId="0"/></cellStyles></styleSheet>`;
  const zip = new JSZip();
  zip.file("[Content_Types].xml", '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>');
  zip.file("_rels/.rels", '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>');
  zip.file("xl/workbook.xml", '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="2表" sheetId="1" r:id="rId1"/></sheets></workbook>');
  zip.file("xl/_rels/workbook.xml.rels", '<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>');
  zip.file("xl/worksheets/sheet1.xml", sheet);
  zip.file("xl/styles.xml", styles);
  fs.writeFileSync(out, await zip.generateAsync({ type: "nodebuffer", compression: "DEFLATE" }));
}

async function aiRole() {
  const ai = path.join(ROOT, "kadai_ai");
  const jobs = fs.readdirSync(path.join(ai, "30_記入係作業中"), { withFileTypes: true }).filter((e) => e.isDirectory());
  if (jobs.length !== 1) throw new Error("30_記入係作業中 の仕事が1つではありません");
  const job = jobs[0].name;
  const srcDir = path.join(ai, "30_記入係作業中", job, "資料");
  const dst = path.join(ai, "40_合格・確認待ち", job);
  fs.mkdirSync(dst, { recursive: true });
  for (const f of fs.readdirSync(srcDir)) {
    const data = fs.readFileSync(path.join(srcDir, f));
    if (f.endsWith(".docx") && f.includes("支援経過")) {
      const zip = await JSZip.loadAsync(data);
      let xml = await zip.file("word/document.xml").async("string");
      const p = `<w:p><w:r><w:t xml:space="preserve">${AI_LINE}</w:t></w:r></w:p>`;
      const k = xml.lastIndexOf("<w:sectPr");
      xml = k >= 0 ? xml.slice(0, k) + p + xml.slice(k) : xml.replace("</w:body>", p + "</w:body>");
      zip.file("word/document.xml", xml);
      fs.writeFileSync(path.join(dst, f), await zip.generateAsync({ type: "nodebuffer", compression: "DEFLATE" }));
    } else {
      fs.writeFileSync(path.join(dst, f), data);
    }
  }
  await makeCarePlan(path.join(dst, CAREPLAN));
  fs.writeFileSync(path.join(dst, "判定記録.md"), `# 判定記録：${job}（動画用のAI役）\n\n- 判定：1回目で合格\n- ${CAREPLAN} を作った（記号のまま）\n- 支援経過記録に1件書き足した（記号のまま）\n`, "utf8");
  log(`AI役：${path.relative(ROOT, dst)} に置きました`);
  return job;
}

// ---------------- 撮影の流れ ----------------

async function scenario(page, url) {
  page.on("dialog", (d) => d.accept());
  await page.goto(url);
  await waitIdle(page);
  await caption(page, "AI作業パネル（課題用・架空のデータだけ）", "ケアマネの書類をAIに渡す前に、個人情報を記号に置き換えて、AIの作業後に元に戻します", 3500);

  // 0. 場所の確かめ：kadai_masking でなければ止める
  await caption(page, "まず、課題用のフォルダで動いていることを確かめます", "", 1500);
  await click(page, page.locator('a[data-page="work"]'));
  await waitIdle(page);
  const where = await page.locator("text=場所：").first().innerText();
  const want = path.join(ROOT, "kadai_masking");
  if (!where.includes(want)) throw new Error(`場所が課題用のフォルダではありません：${where}（期待：${want}）`);
  await moveTo(page, page.locator("text=場所：").first());
  await caption(page, "場所：…\\kadai_panel\\kadai_masking（課題用のフォルダ）", "本物の書類のフォルダではありません", 2800);
  await goTop(page);

  // 準備：利用者・家族の登録
  await click(page, page.locator('.step[data-step="1"]'));
  await waitIdle(page);
  await caption(page, "準備：架空の利用者と家族を登録します", "見本 太郎（利用者）・見本 花子（妻）・見本 一郎（長男）", 2200);
  const reg = page.locator("details.reg", { hasText: "はじめての利用者" });
  if (!(await reg.evaluate((d) => d.open))) await click(page, reg.locator("summary"));
  for (const [kind, name, alias] of [["利用者", "見本 太郎", "ミホン タロウ"], ["家族", "見本 花子", ""], ["家族", "見本 一郎", ""]]) {
    const r = page.locator("details.reg", { hasText: "はじめての利用者" });
    if (!(await r.evaluate((d) => d.open))) await click(page, r.locator("summary"), 300);
    await moveTo(page, r.locator("select"));
    await r.locator("select").selectOption(kind);
    await sleep(300);
    await type(page, r.locator("input").nth(0), name);
    if (alias) await type(page, r.locator("input").nth(1), alias);
    await click(page, r.getByRole("button", { name: "氏名一覧に登録する" }), 400);
    await waitIdle(page);
  }

  // ① 資料を入れる：試し用の書類のカードをドラッグ
  await caption(page, "① 資料を入れる", "試し用の書類のカードを、上の枠へドラッグします", 2000);
  const drop = page.locator(".drop").first();
  for (const n of ["サンプル_フェイスシート.xlsx", "サンプル_支援経過記録.docx"]) {
    const card = page.locator(".sample-card", { hasText: n });
    await moveTo(page, card);
    await card.dragTo(drop);
    await waitIdle(page);
    await page.locator(".drop").first().scrollIntoViewIfNeeded();
    await sleep(1200);
  }
  await moveTo(page, page.locator("#srclist"));
  await caption(page, "2つの書類が「1_元データ」に入りました", "", 2000);

  // ② 置き換えて確認する（①の画面の下のボタンから）
  await caption(page, "② 名前などの個人情報を記号に置き換えます", "", 1500);
  await click(page, page.getByRole("button", { name: /名前などの個人情報を記号に置き換える/ }), 300);
  await waitIdle(page);
  await caption(page, "緑＝記号にした所　橙＝人が確かめる所", "登録していない住所・名前は、AIに渡さずに止めます", 3000);

  // 橙の仕分け：1つ目（住所）はゆっくり見せる
  const addrBtn = page.getByRole("button", { name: "住所として置き換える" }).first();
  await moveTo(page, addrBtn);
  await caption(page, "橙の仕分け：番地まで書かれた住所", "利用者の住所なので「住所として置き換える」を押します → 【住所001】の記号になります", 3200);
  await click(page, addrBtn, 1500);
  await waitIdle(page);
  // 残りは手早く：よその事業所の職員（例田）＝その他、自分の事業所の職員（手本 由紀）＝除外
  await caption(page, "残りの橙も仕分けます", "よその事業所の職員は「その他」、自分の事業所の職員は「除外」", 1800);
  for (let i = 0; i < 8; i++) {
    const open = page.locator("div.ask:not(.decided)");
    if (!(await open.count())) break;
    const box = open.first();
    const text = await box.innerText();
    let btn;
    if (text.includes("例田")) btn = box.getByRole("button", { name: /^その他/ }).first();
    else if (text.includes("手本")) {
      const self = box.getByRole("button", { name: "自分の事業所の職員（除外）" });
      btn = (await self.count()) ? self.first() : box.getByRole("button", { name: /名前ではない（除外）/ }).first();
    } else if (await box.getByRole("button", { name: "住所として置き換える" }).count()) btn = box.getByRole("button", { name: "住所として置き換える" }).first();
    else throw new Error("思っていない橙の箱があります：" + text.slice(0, 80));
    await click(page, btn, 1200);
    await waitIdle(page);
  }
  const again = page.getByRole("button", { name: "もう一度置き換える" });
  if ((await again.count()) && (await again.isEnabled())) await click(page, again, 300);
  await waitIdle(page);
  await page.waitForFunction(() => !document.querySelector(".st.hold"), null, { timeout: 60000 });
  await page.locator(".st.ok").first().scrollIntoViewIfNeeded();
  await caption(page, "橙がなくなり、緑（記号）だけになりました", "見本 太郎 → 【利用者001】、住所 → 【住所001】、電話 → 【電話001】", 3500);
  await page.mouse.wheel(0, 500);
  await sleep(1500);

  // ③ AIに渡す
  await caption(page, "③ AIに渡す", "利用者・書類の種類・日付を選んで「OK・AIに渡す」", 1800);
  const uSel = page.locator("select", { has: page.locator("option", { hasText: "（選んでください）" }) }).first();
  await moveTo(page, uSel);
  const opt = await uSel.locator("option", { hasText: "利用者001" }).first().getAttribute("value");
  await uSel.selectOption(opt);
  await sleep(600);
  await moveTo(page, page.locator("#docsel"));
  await page.locator("#docsel").selectOption("ケアプラン");
  await sleep(900);
  await click(page, page.getByRole("button", { name: "OK・AIに渡す" }), 300);
  await waitIdle(page);
  await caption(page, "記号だけの書類が、AI作業フォルダ（kadai_ai）へ渡りました", "AIへの依頼の書き置きも 00_指示 にできます", 3000);

  // AI役
  await goTop(page);
  await caption(page, "（AIの作業）", "AI役が、記号のままケアプラン（第2表）を作り、支援経過に1件書き足して、40_合格・確認待ち に置きます", 2500);
  await aiRole();
  await sleep(2000);

  // ④ 元に戻す
  await caption(page, "④ 届いた合格品", "パネルが10秒ごとに確かめて、届いた書類を表示します", 1500);
  await click(page, page.locator('.step[data-step="4"]'));
  const back = page.getByRole("button", { name: "記号を名前などの元の個人情報に戻す" }).first();
  for (let i = 0; i < 40 && !(await back.count()); i++) {
    await sleep(1000);
    if (i % 10 === 9) await page.evaluate(() => load());
  }
  await waitIdle(page);
  await caption(page, "記号を、元の名前・住所に戻します", "", 1800);
  await click(page, back, 300);
  await waitIdle(page);
  await caption(page, "元の個人情報に戻した書類が「2_完成」に入りました", "", 2500);

  // ⑥ 完成した書類：このあと Excel・Word で開いて見せる
  await goTop(page);
  await click(page, page.locator('.step[data-step="6"]'));
  await waitIdle(page);
  await caption(page, "⑥ 完成した書類", "ケアプラン（第2表）・フェイスシート・支援経過記録の3つができました", 2500);
  const row = page.locator("div", { hasText: "サンプル_フェイスシート.xlsx" }).filter({ has: page.getByRole("button", { name: "開く" }) }).last();
  if (await row.count()) await moveTo(page, row.getByRole("button", { name: "開く" }).first());
  await caption(page, "できた書類を、Excel・Word で開いて確かめます", "", 2500);
}

// ---------------- 字幕のカード・Office の場面・つなぎ合わせ ----------------

const CARD_CSS = `body{margin:0;width:${W}px;height:${H}px;display:flex;align-items:center;justify-content:center;
  background:linear-gradient(135deg,#fdf2f4,#f6e3ea);font-family:"Yu Gothic UI","Meiryo",sans-serif;color:#3a2a33}
  .c{text-align:center;max-width:1000px}.t{font-size:44px;font-weight:700;margin-bottom:18px}.s{font-size:24px;line-height:1.7;color:#6b5560}`;
const BAR_CSS = `body{margin:0;width:${W}px;height:${H}px;background:transparent}
  #b{position:fixed;left:50%;bottom:22px;transform:translateX(-50%);max-width:86%;background:rgba(20,30,45,.88);color:#fff;
  font:600 22px/1.5 "Yu Gothic UI","Meiryo",sans-serif;padding:12px 26px;border-radius:12px;text-align:center}
  #b small{display:block;font-weight:400;font-size:16px;opacity:.85}`;
const esc = (t) => t.replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[c]);

async function makePng(browser, out, html, transparent) {
  const page = await browser.newPage({ viewport: { width: W, height: H } });
  await page.setContent(html);
  await page.screenshot({ path: out, omitBackground: !!transparent });
  await page.close();
}
const card = (b, out, t, s) => makePng(b, out, `<style>${CARD_CSS}</style><div class="c"><div class="t">${esc(t)}</div><div class="s">${s.split("\n").map(esc).join("<br>")}</div></div>`);
const bar = (b, out, t, s) => makePng(b, out, `<style>${BAR_CSS}</style><div id="b">${esc(t)}<small>${esc(s)}</small></div>`, true);

function officeReady(kind) {
  try { execFileSync("reg", ["query", `HKCR\\${kind}.Application`], { stdio: "ignore" }); return true; } catch { return false; }
}

// Office で開いて、全画面表示にした窓が占める画面の範囲だけを ffmpeg で撮る（範囲の外は撮らない）。
// Office は GPU で描くので、窓を名指し（title=）で撮ると白くなるため、画面の範囲で撮る
function officeClip(file, out, o = {}) {
  return new Promise((resolve, reject) => {
    const ps = spawn("powershell.exe", ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path.join(DEMO, "office_show.ps1"), "-Path", file, "-Pace", FAST ? "0.3" : "1",
      ...(o.cells ? ["-Cells", o.cells, "-Whole", o.whole, "-Zoom", String(o.zoom)] : []),
      ...(o.panTo ? ["-PanTo", String(o.panTo), "-Cells2", o.cells2] : [])], { windowsHide: true });
    let ff = null, buf = "", err = "";
    ps.stderr.on("data", (d) => (err += d));
    ps.stdout.on("data", (d) => {
      buf += d.toString("utf8");
      let i;
      while ((i = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, i).trim(); buf = buf.slice(i + 1);
        if (line.startsWith("READY|") && !ff) {
          const [, rect, title] = line.split("|");
          let [x, y, w, h] = rect.split(",").map(Number); // 窓が載っている画面の内側に収めた範囲（office_show.ps1）
          w -= w % 2; h -= h % 2;
          log(`  撮る範囲：${w}x${h}（${title}）`);
          ff = spawn(ffmpegPath, ["-y", "-loglevel", "error", "-f", "gdigrab", "-framerate", "25", "-draw_mouse", "0",
            "-offset_x", String(x), "-offset_y", String(y), "-video_size", `${w}x${h}`, "-i", "desktop",
            "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", "matroska", out], { stdio: ["pipe", "ignore", "pipe"] });
          ff.stderr.on("data", (d) => (err += d));
        } else if (line === "DONE" && ff) {
          ff.stdin.end("q\n");
        }
      }
    });
    ps.on("exit", (code) => {
      const fin = () => (code === 0 && fs.existsSync(out) && fs.statSync(out).size > 0 ? resolve() : reject(new Error("Office の場面を撮れませんでした：" + (err.trim() || "撮影が始まりませんでした"))));
      if (ff && ff.exitCode === null) ff.on("exit", fin); else fin();
    });
  });
}

// 部品（ブラウザの動画・カード・Office の場面＋字幕）を 1280x800・25fps にそろえて1本につなぐ
function compose(parts, mp4) {
  const inputs = [], chains = [];
  let n = 0;
  const fit = `scale=${W}:${H}:force_original_aspect_ratio=decrease,pad=${W}:${H}:(ow-iw)/2:(oh-ih)/2:color=0x2b2b2b,fps=25,setsar=1`;
  parts.forEach((p, k) => {
    if (p.png) {
      inputs.push("-loop", "1", "-t", String(p.sec), "-i", p.png);
      chains.push(`[${n++}:v]${fit},format=yuv420p[v${k}]`);
    } else if (p.bar) {
      inputs.push("-i", p.video, "-i", p.bar);
      chains.push(`[${n}:v]${fit}[b${k}];[b${k}][${n + 1}:v]overlay=0:0,format=yuv420p[v${k}]`);
      n += 2;
    } else {
      inputs.push("-i", p.video);
      chains.push(`[${n++}:v]${fit},format=yuv420p[v${k}]`);
    }
  });
  const fc = chains.join(";") + ";" + parts.map((_, k) => `[v${k}]`).join("") + `concat=n=${parts.length}:v=1:a=0[out]`;
  execFileSync(ffmpegPath, ["-y", "-loglevel", "error", ...inputs, "-filter_complex", fc, "-map", "[out]", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "22", "-movflags", "+faststart", mp4]);
}

const OFFICE = [
  { file: CAREPLAN, kind: "Excel", title: "ケアプラン（第2表）を Excel で開きます",
    sub: "AIが記号のまま作ったケアプランです。\n課題・目標・サービス内容の名前が、元の名前に戻っているか確かめます",
    bar: ["ケアプラン（第2表）：AIが作り、元の名前に戻しました", "利用者名「見本太郎 殿」、課題・目標の「花子さん」、受診に同行する「一郎さん」（すべて架空）"],
    // 拡大して見せる：左（利用者名・課題・目標）→ 右へ動かす（サービス内容・種別・事業所・頻度・期間）→ 左へ戻す
    cells: "E3,A7,B7,D7", cells2: "F7,H7,I7,J7,K7,F10,H11", panTo: 6, whole: "A7:E12", zoom: 140 },
  { file: "サンプル_フェイスシート.xlsx", kind: "Excel", title: "サンプル_フェイスシート.xlsx を Excel で開きます",
    sub: "氏名・フリガナ・生年月日・住所・電話・被保険者番号が\n元の文字に戻っているか、確かめます",
    bar: ["Excel：記号が元の個人情報に戻っています", "見本太郎／昭和15年3月2日／〒999-9999 架空県虹彩市星降町…／000-0000-0000（すべて架空）"] },
  { file: "サンプル_支援経過記録.docx", kind: "Word", title: "サンプル_支援経過記録.docx を Word で開きます",
    sub: "本文の名前と、AIが書き足した最後の1件が\n元の名前に戻っているか、確かめます",
    bar: ["Word：AIが書き足した1件も、元の名前に戻っています", "「【利用者001】様…【家族001の名】さん…」→「見本太郎様…花子さん…」"] },
];

// ---------------- 本体 ----------------

async function main() {
  if (!fs.existsSync(SAMPLES)) throw new Error("samples フォルダがありません");
  const officeOnly = args.has("--office-only"); // 試し用：ブラウザの場面を撮らず、2_完成 の書類で Office の場面だけ撮る
  if (!officeOnly) prepareFolders();
  const rawDir = path.join(VIDEOS, "_raw_" + stamp());
  fs.mkdirSync(rawDir, { recursive: true });
  const parts = [];
  let browser, proc = null, ok = false;
  try {
    browser = await chromium.launch({ channel: "chrome", headless: !args.has("--show") });
    if (!officeOnly) {
      log("パネルを起動しています（run_source.bat。初回は組み立てに少しかかります）…");
      const started = await startPanel();
      proc = started.proc;
      const ctx = await browser.newContext({ viewport: { width: W, height: H }, recordVideo: { dir: rawDir, size: { width: W, height: H } }, locale: "ja-JP" });
      const page = await ctx.newPage();
      try {
        await scenario(page, started.url);
      } catch (e) {
        await page.screenshot({ path: path.join(VIDEOS, "失敗した画面_" + stamp() + ".png") }).catch(() => {});
        throw e;
      } finally {
        await ctx.close();
        stopPanel(proc);
      }
      parts.push({ video: path.join(rawDir, fs.readdirSync(rawDir).find((f) => f.endsWith(".webm"))) });
    }
    // Office の場面（Excel・Word が入っていれば）
    const done = path.join(ROOT, "kadai_masking", "2_完成");
    for (const [k, o] of OFFICE.entries()) {
      const file = path.join(done, o.file);
      if (!fs.existsSync(file)) throw new Error("2_完成 に " + o.file + " がありません");
      if (!officeReady(o.kind)) { log(`${o.kind} が入っていないので、${o.file} の場面は飛ばします`); continue; }
      const cardPng = path.join(rawDir, `card${k}.png`), barPng = path.join(rawDir, `bar${k}.png`), clip = path.join(rawDir, `office${k}.mkv`);
      await card(browser, cardPng, `成果物 ${k + 1}／${OFFICE.length}　${o.title}`, o.sub);
      await bar(browser, barPng, o.bar[0], o.bar[1]);
      log(`${o.kind} で ${o.file} を開いて撮っています（この間はパソコンを触らないでください）…`);
      await officeClip(file, clip, o);
      parts.push({ png: cardPng, sec: FAST ? 1 : 3 }, { video: clip, bar: barPng });
    }
    const endPng = path.join(rawDir, "end.png");
    await card(browser, endPng, "パソコンの外には出さず、AIには記号だけを渡しました", "名前・住所・電話番号は、AIの作業のあとで元に戻ります\n（登場する人物・住所・番号はすべて架空です）");
    parts.push({ png: endPng, sec: FAST ? 1.5 : 4.5 });
    ok = true;
  } finally {
    if (browser) await browser.close();
    stopPanel(proc);
  }
  const mp4 = path.join(VIDEOS, `panel_demo_${stamp()}${officeOnly ? "_office" : ""}.mp4`);
  log("動画をつなげています…");
  compose(parts, mp4);
  if (ok) fs.rmSync(rawDir, { recursive: true, force: true });
  log("動画：" + path.relative(ROOT, mp4));
}

main().catch((e) => {
  console.error("止まりました：" + (e && e.message ? e.message : e));
  process.exit(1);
});
