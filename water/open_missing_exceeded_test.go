package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对超标标记本身是否明确保存的核对：
// 已确认样品，以及已确认后作废、仍保留逐项判定的样品，其整份样品与每条逐项
// 判定中的 exceeded 都必须实际保存为布尔值。exceeded 字段缺失或保存为 null，
// JSON 反序列化后都落成 false，与明确保存的 false 无法区分；不能把“缺少
// 结论”当成一份已保存的达标结论——即使测量值、上限、生效时间齐全且按这些
// 依据比较会得到达标，即使其余项目标记完整，也必须让整次 Open 失败。
// 明确保存的 false 和 true 都属于已保存标记，仍按原规则核对它们与依据是否
// 一致。待判定样品、未确认即作废且没有逐项判定的样品不要求提前保存有效结论。

// assertOpenRejectsMissingExceeded 断言打开整次失败：返回 nil 存放、错误可
// 识别为 ErrCorruptRecord、点名样品编号、原文件字节不变；whole 为 true 时
// 要求信息说明缺少的是整份超标标记，否则要求说明缺少逐项超标标记并点出项目。
func assertOpenRejectsMissingExceeded(t *testing.T, dir, sampleID string, whole bool, item string) {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file before open: %v", err)
	}
	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("缺少超标标记必须让整次 Open 失败，sample=%s whole=%v", sampleID, whole)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, sampleID) {
		t.Fatalf("错误信息应指出样品编号 %q，实际 %q", sampleID, msg)
	}
	if !strings.Contains(msg, "exceeded") {
		t.Fatalf("错误信息应指出 exceeded 字段，实际 %q", msg)
	}
	if whole {
		if !strings.Contains(msg, "整份超标标记") {
			t.Fatalf("错误信息应说明缺少的是整份超标标记，实际 %q", msg)
		}
	} else {
		if !strings.Contains(msg, "逐项超标标记") {
			t.Fatalf("错误信息应说明缺少的是逐项超标标记，实际 %q", msg)
		}
		if item != "" && !strings.Contains(msg, item) {
			t.Fatalf("错误信息应指出缺少逐项标记的项目 %q，实际 %q", item, msg)
		}
	}
	after, rerr := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if rerr != nil {
		t.Fatalf("read file after open: %v", rerr)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}

// exceededToken 把“如何写 exceeded”转成 JSON 片段尾部：
// "false"/"true" 为明确保存的布尔值，"null" 保存为 null，空串表示整个字段缺失。
func exceededToken(tok string) string {
	if tok == "" {
		return ""
	}
	return `, "exceeded": ` + tok
}

// missingExceededFile 构造一份 pH 7、上限 8（达标依据完整）的记录原始 JSON。
// wholeTok/itemTok 控制整份与 pH 逐项 exceeded 写成什么（"false"、"null" 或
// 空串缺失）；multi 为 true 时增加一个浊度 4=4 达标项，其逐项标记明确为 false。
func missingExceededFile(t *testing.T, status, voidReason, wholeTok, itemTok string, multi bool) string {
	t.Helper()
	eff := openEff().Format(time.RFC3339Nano)
	sampled := at(10, 0).Format(time.RFC3339Nano)
	pHResult := `{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` + eff + `"` + exceededToken(itemTok) + `}`
	meas := `[{"item": "pH", "value": 7}]`
	results := "[" + pHResult + "]"
	if multi {
		meas = `[{"item": "pH", "value": 7}, {"item": "浊度", "value": 4}]`
		turbResult := `{"item": "浊度", "value": 4, "limit": 4, "limitEffective": "` + eff + `", "exceeded": false}`
		results = "[" + pHResult + ", " + turbResult + "]"
	}
	voidField := ""
	if voidReason != "" {
		voidField = `, "voidReason": "` + voidReason + `"`
	}
	return `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + sampled + `",
      "measurements": ` + meas + `,
      "results": ` + results + `,
      "status": "` + status + `"` + voidField + exceededToken(wholeTok) + `
    }
  }
}`
}

// 题目给出的核心例子：pH 7、上限 8，逐项标记明确为 false，但整份标记被删掉。
// 即使按依据比较整份本应达标，缺少结论也必须让整次打开失败。
func TestOpenConfirmedMissingWholeExceeded(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "confirmed", "", "", "false", false))
	assertOpenRejectsMissingExceeded(t, dir, "S1", true, "")
}

// 整份 exceeded 保存为 null 与字段缺失一样是结论残缺，整次打开失败。
func TestOpenConfirmedNullWholeExceeded(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "confirmed", "", "null", "false", false))
	assertOpenRejectsMissingExceeded(t, dir, "S1", true, "")
}

// 整份标记存在，但唯一一条逐项判定的 exceeded 被删掉：反方向同样必须失败，
// 错误点出样品与项目。
func TestOpenConfirmedMissingItemExceeded(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "confirmed", "", "false", "", false))
	assertOpenRejectsMissingExceeded(t, dir, "S1", false, openPH)
}

// 逐项 exceeded 保存为 null 同样是结论残缺。
func TestOpenConfirmedNullItemExceeded(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "confirmed", "", "false", "null", false))
	assertOpenRejectsMissingExceeded(t, dir, "S1", false, openPH)
}

// 多项目样品只缺 pH 一项的逐项标记，浊度标记完整、整份标记也在：
// 仍属于结论残缺，整份拒绝并点名 pH。
func TestOpenMultiItemOneMissingItemExceeded(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "confirmed", "", "false", "", true))
	assertOpenRejectsMissingExceeded(t, dir, "S1", false, openPH)
}

// 多项目样品的逐项标记都明确保存，但整份标记缺失：即使全部达标、按依据
// 汇总本应为 false，也不能把缺少整份标记当成已保存的达标结论。
func TestOpenMultiItemMissingWholeExceeded(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "confirmed", "", "", "false", true))
	assertOpenRejectsMissingExceeded(t, dir, "S1", true, "")
}

// 测量值实际超标（9 > 8）但单项标记缺失：缺结论就是缺结论，首先应以
// “缺少逐项超标标记”拒绝，而不是把读成的 false 拿去与依据核对。
func TestOpenMissingItemExceededEvenWhenValueOverLimit(t *testing.T) {
	dir := t.TempDir()
	eff := openEff().Format(time.RFC3339Nano)
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "results": [{"item": "pH", "value": 9, "limit": 8, "limitEffective": "` + eff + `"}],
      "status": "confirmed", "exceeded": true
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingExceeded(t, dir, "S1", false, openPH)
}

// 已确认后作废、仍保留逐项判定的样品：整份标记缺失同样拒绝——作废只取消
// 有效资格，保留下来的历史结论也必须完整。
func TestOpenVoidedAfterConfirmedMissingWholeExceeded(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "voided", "复测确认样品污染", "", "false", false))
	assertOpenRejectsMissingExceeded(t, dir, "S1", true, "")
}

// 已确认后作废、仍保留逐项判定的样品：逐项标记缺失同样拒绝并点名项目。
func TestOpenVoidedAfterConfirmedMissingItemExceeded(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "voided", "复测确认样品污染", "false", "", false))
	assertOpenRejectsMissingExceeded(t, dir, "S1", false, openPH)
}

// 同一文件里另一份样品完整正常，也不能只读入正常部分：整次失败、无可用存放。
func TestOpenMissingExceededRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S-GOOD": {
      "id": "S-GOOD", "pointId": "P1",
      "sampledAt": "` + at(5, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` + openEff().Format(time.RFC3339Nano) + `", "exceeded": false}],
      "status": "confirmed", "exceeded": false
    },
    "S-BAD": {
      "id": "S-BAD", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` + openEff().Format(time.RFC3339Nano) + `", "exceeded": false}],
      "status": "confirmed"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingExceeded(t, dir, "S-BAD", true, "")
}

// 明确保存的 false 仍是合法标记：两处 exceeded 都显式写 false 的达标记录
// （原始 JSON 中字段确实存在）必须正常读入，结论原样保留，可作为最近有效结果。
func TestOpenExplicitFalseExceededLoads(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, missingExceededFile(t, "confirmed", "", "false", "false", false))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("明确保存的 false 是已保存标记，应正常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("正常达标记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	if latest.Exceeded || len(latest.Results) != 1 || latest.Results[0].Exceeded {
		t.Fatalf("明确保存的达标结论应原样保留: %+v", latest)
	}
}

// 待判定样品没有结论：原始 JSON 中整个 exceeded 与 results 都缺失也照常读入，
// 读入后仍是待判定、没有结论。
func TestOpenPendingMissingExceededFieldLoads(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("待判定样品不要求提前保存有效结论，应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	if list[0].Status != StatusPending || list[0].Results != nil {
		t.Fatalf("待判定样品读入后不应带结论: %+v", list[0])
	}
}

// 未确认即作废、没有逐项判定的样品：exceeded 字段缺失也照常读入，不凭空
// 获得结论，也不参与最近有效结果。
func TestOpenVoidedFromPendingMissingExceededFieldLoads(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "voided", "voidReason": "录入信息有误"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("未确认即作废且无逐项判定的样品不要求超标标记，应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	if list[0].Status != StatusVoided || len(list[0].Results) != 0 || list[0].Exceeded {
		t.Fatalf("该作废记录不应凭空获得结论: %+v", list[0])
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("作废记录不应参与最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
}
