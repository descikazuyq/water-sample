package water

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对样品状态字段的完整性核对：每份已保存样品都
// 必须明确写有现有的三种状态之一（pending、confirmed、voided）。status 字段
// 缺失、保存为 null、为空字符串，或写成其他任何字符串（含带首尾空白、大小写
// 不同的写法），都按损坏记录处理：整次 Open 失败、返回 nil 存放、错误可识别为
// ErrCorruptRecord 并点名样品编号，且区分状态缺失与不支持的状态值，未知字符串
// 在说明中原样带出。状态决定样品能否产生有效结论，原测量完整、逐项判定齐全或
// 留有作废原因都不能代替明确的状态；不能自动整理后接受、不补状态、不只读入
// 同文件的其他正常样品、不改写原文件。

// assertOpenRejectsStatus 断言打开失败并点名样品，且错误信息包含 want 文本。
func assertOpenRejectsStatus(t *testing.T, dir, sampleID string, want ...string) {
	t.Helper()
	assertOpenRejects(t, dir, sampleID, "", true)
	_, err := Open(dir)
	if err == nil {
		t.Fatalf("损坏数据必须让整次 Open 失败，sample=%s", sampleID)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Fatalf("错误信息应包含 %q，实际 %q", w, msg)
		}
	}
}

// statusSampleJSON 生成一份 pH 原测量为 7 的样品记录，statusFragment 是落盘的
// status 字段片段（含为空，表示整个字段缺失）。样品带有完整的逐项判定与作废
// 原因，用来证明这些字段都不能代替明确的状态。
func statusSampleJSON(statusFragment string) string {
	return `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      ` + statusFragment + `
      "results": [{"item": "pH", "value": 7, "limit": 8,
        "limitEffective": "` + at(1, 0).Format(time.RFC3339Nano) + `",
        "exceeded": false}],
      "exceeded": false,
      "voidReason": "样品污染"
    }
  }
}`
}

// status 字段整个缺失：编号、采样时间、原测量、逐项判定与作废原因都齐全，
// 也不能据此猜出它原来是否作废，整次打开失败并说明状态缺失。
func TestOpenMissingStatusField(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, statusSampleJSON(""))
	assertOpenRejectsStatus(t, dir, "S1", "缺少状态", "status")
}

// status 保存为 null 与字段缺失一样是状态缺失，null 不是待判定。
func TestOpenNullStatus(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, statusSampleJSON(`"status": null,`))
	assertOpenRejectsStatus(t, dir, "S1", "缺少状态")
}

// status 为空字符串同样是状态缺失，不能当成待判定接受。
func TestOpenEmptyStatus(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, statusSampleJSON(`"status": "",`))
	assertOpenRejectsStatus(t, dir, "S1", "缺少状态")
}

// status 写成不支持的字符串：错误说明应原样带出该字符串以便辨认，
// 且与状态缺失的说明区分开。
func TestOpenUnknownStatus(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, statusSampleJSON(`"status": "archived",`))
	assertOpenRejectsStatus(t, dir, "S1", "不受支持", "archived")
}

// 带首尾空白的状态写法不合法：不能自动整理成 pending 后接受。
func TestOpenStatusWithWhitespace(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, statusSampleJSON(`"status": " pending ",`))
	assertOpenRejectsStatus(t, dir, "S1", "不受支持", " pending ")
}

// 大小写不同的状态写法不合法：不能自动整理成 confirmed 后接受。
func TestOpenStatusWrongCase(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, statusSampleJSON(`"status": "Confirmed",`))
	assertOpenRejectsStatus(t, dir, "S1", "不受支持", "Confirmed")
}

// 同一文件中其他样品状态正常，也不能只读入正常部分：一份损坏整次失败。
func TestOpenBadStatusRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"
    },
    "S2": {
      "id": "S2", "pointId": "P1",
      "sampledAt": "` + at(11, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 6}],
      "status": "done"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsStatus(t, dir, "S2", "不受支持", "done")
}

// 三种合法状态（待判定、已确认、待判定后作废、已确认后作废）原样读入，
// 已确认样品保留旧判定依据，已作废样品保留原因且不能再次确认。
func TestOpenValidStatusesAccepted(t *testing.T) {
	dir := t.TempDir()
	eff := at(1, 0).Format(time.RFC3339Nano)
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"
    },
    "S2": {
      "id": "S2", "pointId": "P1",
      "sampledAt": "` + at(11, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed",
      "results": [{"item": "pH", "value": 9, "limit": 8,
        "limitEffective": "` + eff + `", "exceeded": true}],
      "exceeded": true
    },
    "S3": {
      "id": "S3", "pointId": "P1",
      "sampledAt": "` + at(12, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 6}],
      "status": "voided",
      "voidReason": "录入信息有误"
    },
    "S4": {
      "id": "S4", "pointId": "P1",
      "sampledAt": "` + at(13, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "voided",
      "results": [{"item": "pH", "value": 9, "limit": 8,
        "limitEffective": "` + eff + `", "exceeded": true}],
      "exceeded": true,
      "voidReason": "复测确认样品污染"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("三种合法状态应正常打开，got %v", err)
	}
	defer st.Close()
	list, err := st.ListByPoint("P1")
	if err != nil {
		t.Fatalf("ListByPoint: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("应读入 4 份样品，got %d", len(list))
	}
	got := map[string]Sample{}
	for _, smp := range list {
		got[smp.ID] = smp
	}
	if got["S1"].Status != StatusPending || got["S1"].Results != nil {
		t.Fatalf("S1 应为无判定的待判定样品，got %+v", got["S1"])
	}
	if got["S2"].Status != StatusConfirmed || !got["S2"].Exceeded || len(got["S2"].Results) != 1 {
		t.Fatalf("S2 应为保留依据的已确认样品，got %+v", got["S2"])
	}
	if got["S3"].Status != StatusVoided || got["S3"].VoidReason != "录入信息有误" {
		t.Fatalf("S3 应为待判定后作废的样品，got %+v", got["S3"])
	}
	if got["S4"].Status != StatusVoided || len(got["S4"].Results) != 1 ||
		got["S4"].VoidReason != "复测确认样品污染" {
		t.Fatalf("S4 应为确认后作废、保留原判定的样品，got %+v", got["S4"])
	}
	if _, err := st.Confirm("S4"); !errors.Is(err, ErrVoided) {
		t.Fatalf("确认后作废的样品不能再次确认，got %v", err)
	}
	latest, ok, err := st.LatestResult("P1")
	if err != nil {
		t.Fatalf("LatestResult: %v", err)
	}
	if !ok || latest.ID != "S2" {
		t.Fatalf("最近有效结果应为 S2，got %v, %v", latest, ok)
	}
}
