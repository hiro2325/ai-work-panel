# 完成した書類を Excel・Word で開いて見せる（動画用。record.mjs から呼ぶ）。
# 読み取り専用で開き、全画面表示（上の帯・リボン・アカウント名を隠す）にして、見せる所を順に選ぶ。保存せずに閉じる。
# 標準出力に「READY|左,上,幅,高さ|窓の名前」を出したら撮り始めてよい合図、「DONE」で撮り終わりの合図。
# Office は GPU で描くので、窓を名指しで撮ると白くなる。全画面表示にした窓が占める画面の範囲を撮る（範囲の外は撮らない）。
param(
  [Parameter(Mandatory = $true)][string]$Path,
  [double]$Pace = 1.0,
  [string]$Cells = "B4,B5,B6,B7,B8,B9,B12,B13",   # Excel：順に選んで見せるセル
  [string]$Whole = "A3:B16",                     # Excel：最後に選ぶ範囲
  [int]$Zoom = 150,
  [int]$PanTo = 0,                               # Excel：右へ動かす先の列（0＝動かさない）。Cells のあと右へ、Cells2 のあと左へ戻す
  [string]$Cells2 = ""                           # Excel：右へ動かしたあとに選ぶセル
)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
Add-Type @"
using System; using System.Text; using System.Runtime.InteropServices;
public static class W32 {
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int n);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint f);
  [DllImport("user32.dll")] public static extern int GetWindowLong(IntPtr h, int i);
  [DllImport("user32.dll")] public static extern void keybd_event(byte k, byte s, uint f, UIntPtr e);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L, T, R, B; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  public static string Rect(IntPtr h){ RECT r; GetWindowRect(h, out r); return r.L+","+r.T+","+(r.R-r.L)+","+(r.B-r.T); }
  public static string Title(IntPtr h){ var s=new StringBuilder(512); GetWindowText(h,s,512); return s.ToString(); }
}
"@
[W32]::SetProcessDPIAware() | Out-Null   # 画面の範囲を、実際の画素で知らせるため
Add-Type -AssemblyName System.Windows.Forms
function Wait([double]$sec) { Start-Sleep -Milliseconds ([int]($sec * 1000 * $Pace)) }
function Ready([IntPtr]$h) {
  [W32]::ShowWindow($h, 3) | Out-Null   # 最大化
  # 一番手前に出す：Windows は別のアプリを勝手に手前へ出さないので、Alt キーを1回押した扱いにしてから手前に出し、「常に手前」にする
  for ($i = 0; $i -lt 5; $i++) {
    [W32]::keybd_event(0x12, 0, 0, [UIntPtr]::Zero); [W32]::keybd_event(0x12, 0, 2, [UIntPtr]::Zero)
    [W32]::SetForegroundWindow($h) | Out-Null
    [W32]::SetWindowPos($h, [IntPtr](-1), 0, 0, 0, 0, 0x0043) | Out-Null   # HWND_TOPMOST・NOMOVE|NOSIZE|SHOWWINDOW
    Start-Sleep -Milliseconds 600
    $top = ([W32]::GetWindowLong($h, -20) -band 0x8) -ne 0          # WS_EX_TOPMOST
    if ($top -and [W32]::GetForegroundWindow() -eq $h) { break }
  }
  # 手前に出たことを確かめられなければ撮らない（ほかの窓を撮らないため）
  if (-not $top -or [W32]::GetForegroundWindow() -ne $h) { throw "Office の窓を一番手前に出せませんでした。撮らずに止めます" }
  Start-Sleep -Milliseconds 1200
  # 窓の範囲を、窓が載っている画面の内側に収める（全画面表示の窓は画面より数画素はみ出すため）
  $r = [W32]::Rect($h).Split(',') | ForEach-Object { [int]$_ }
  $m = [System.Windows.Forms.Screen]::FromHandle($h).Bounds
  $x1 = [Math]::Max($r[0], $m.Left); $y1 = [Math]::Max($r[1], $m.Top)
  $x2 = [Math]::Min($r[0] + $r[2], $m.Right); $y2 = [Math]::Min($r[1] + $r[3], $m.Bottom)
  Write-Output ("READY|" + "$x1,$y1,$($x2 - $x1),$($y2 - $y1)" + "|" + [W32]::Title($h))
  [Console]::Out.Flush()
  Start-Sleep -Milliseconds 1500   # 撮り始めるのを待つ
}

$full = (Resolve-Path -LiteralPath $Path).Path
if ($full -notmatch '\\kadai_masking\\2_完成\\') { throw "課題用フォルダ（kadai_masking\2_完成）の書類だけを開きます：$full" }

if ($full -match '\.xlsx$') {
  $xl = New-Object -ComObject Excel.Application
  try {
    $xl.DisplayAlerts = $false
    $wb = $xl.Workbooks.Open($full, 0, $true)
    $xl.Visible = $true
    $xl.WindowState = -4137
    $xl.DisplayFullScreen = $true
    $xl.ActiveWindow.Zoom = $Zoom
    $ws = $wb.Worksheets.Item(1)
    $ws.Range("A1").Select() | Out-Null
    Ready ([IntPtr]$xl.Hwnd)
    Wait 1.5
    # 見せるセル（フェイスシートなら氏名・フリガナ・生年月日・住所・電話・被保険者番号・家族構成・緊急連絡先）
    $win = $xl.ActiveWindow
    foreach ($a in $Cells.Split(',')) { $ws.Range($a.Trim()).Select() | Out-Null; Wait 1.1 }
    if ($PanTo -gt 1) {
      # 拡大した表を、1列ずつ右へ動かして見せる → 右側のセルを選ぶ → 左へ戻す
      for ($c = $win.ScrollColumn + 1; $c -le $PanTo; $c++) { $win.ScrollColumn = $c; Wait 0.45 }
      foreach ($a in ($Cells2.Split(',') | Where-Object { $_.Trim() })) { $ws.Range($a.Trim()).Select() | Out-Null; Wait 1.1 }
      for ($c = $win.ScrollColumn - 1; $c -ge 1; $c--) { $win.ScrollColumn = $c; Wait 0.45 }
      Wait 0.8
    }
    if ($Whole) { $win.ScrollColumn = 1; $ws.Range($Whole).Select() | Out-Null }
    Wait 2
    Write-Output "DONE"; [Console]::Out.Flush()
    Start-Sleep -Milliseconds 800
  } finally {
    try { $xl.DisplayFullScreen = $false } catch {}
    try { $wb.Close($false) } catch {}
    $xl.Quit()
    [Runtime.InteropServices.Marshal]::ReleaseComObject($xl) | Out-Null
  }
} elseif ($full -match '\.docx$') {
  $wd = New-Object -ComObject Word.Application
  try {
    $wd.DisplayAlerts = 0
    $doc = $wd.Documents.Open($full, $false, $true)
    $wd.Visible = $true
    $wd.WindowState = 1
    $win = $wd.ActiveWindow
    $win.View.Type = 3   # 印刷レイアウト
    $win.View.Zoom.Percentage = 130
    try { $win.View.FullScreen = $true } catch {}
    $doc.Range(0, 0).Select()
    Ready ([IntPtr]$win.Hwnd)
    Wait 1.5
    # 本文の名前 → AI が書き足した最後の1件
    foreach ($w in @("見本太郎", "花子さん", "一郎さん")) {
      $r = $doc.Content
      if ($r.Find.Execute($w)) { $r.Select(); $win.ScrollIntoView($r); Wait 1.4 }
    }
    $r = $doc.Content
    if ($r.Find.Execute("【ケアプラン作成】")) {
      $r.Expand(4) | Out-Null   # 段落ごと
      $r.Select(); $win.ScrollIntoView($r)
      $win.SmallScroll(12) | Out-Null   # 字幕の帯に隠れないよう、画面の中ほどへ送る
      Wait 3.5
    }
    Write-Output "DONE"; [Console]::Out.Flush()
    Start-Sleep -Milliseconds 800
  } finally {
    try { $win.View.FullScreen = $false } catch {}
    try { $doc.Close(0) } catch {}
    $wd.Quit()
    [Runtime.InteropServices.Marshal]::ReleaseComObject($wd) | Out-Null
  }
} else { throw "Excel・Word の書類ではありません：$full" }
