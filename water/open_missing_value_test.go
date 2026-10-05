package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对测量值本身的完整性核对：
// 每个原测量项目的 value、每条逐项判定的 value 都必须实际写入——字段缺失或保存
// 为 null 都不能被读成零。项目名还在不代表数值完整：待判定样品不能凭读出的零
// 完成确认，已确认（含已确认后作废）样品在原测量与逐项判定都读出零时也不能被
// 当成有效的达标结果。两处同时缺失、缺失后恰好与另一处明确保存的零一致、或按
// 零与上限比较正好得到已有的达标标记，都不能成为接受理由；多项目样品只缺一项
// 的数值也整份拒绝。拒绝时返回 nil 存放、错误可识别为 ErrCorruptRecord，信息
// 点名样品编号与项目，并区分缺少的是“原测量值”还是“逐项判定中的测量值”；
// 同文件其他完整样品不能被部分读入，原文件保持原样。明确保存的零（以及正数、
// 负数）仍是合法测量值，原测量与判定都明确为零的完整记录照常读入。

// assertOpenRejectsValue 在 assertOpenRejects 的基础上进一步区分缺的是原测量值
// 还是逐项判定中的测量值：错误正文（去掉哨兵枚举文案后）应包含 want 中的片段，
// 且不应包含 notWant 中的片段。
func assertOpenRejectsValue(t *testing.T, dir, sampleID, item string, want, notWant []string) {
	t.Helper()
	assertOpenRejects(t, dir, sampleID, item, false)
	_, err := Open(dir)
	if err == nil {
		t.Fatalf("损坏数据必须让整次 Open 失败，sample=%s", sampleID)
	}
	msg := strings.TrimPrefix(err.Error(), ErrCorruptRecord.Error())
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Fatalf("错误信息应包含 %q，实际 %q", w, msg)
		}
	}
	for _, nw := range notWant {
		if strings.Contains(msg, nw) {
			t.Fatalf("错误信息不应包含 %q，实际 %q", nw, msg)
		}
	}
}

func rawSample(sampleID string, body string) string {
	return `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "` + sampleID + `": ` + body + `
  }
}`
}

// 原测量的 value 字段整个缺失：判定里明确保存着零，单项与整份都达标，按读出的
// 零本来能对上，但缺字段不是数值零，整次打开必须失败并说明缺的是原测量值。
func TestOpenConfirmedMeasurementValueFieldMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH"}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openPH,
		[]string{"原测量值"}, []string{"逐项判定"})
}

// 原测量的 value 保存为 null，与字段缺失同样拒绝：null 不是数值零。
func TestOpenConfirmedMeasurementValueNull(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": null}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openPH,
		[]string{"原测量值"}, []string{"逐项判定"})
}

// 待判定样品缺原测量值：不能让它随后凭读出的零完成确认，打开时直接拒绝。
func TestOpenPendingMeasurementValueMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}, {"item": "浊度"}],
      "status": "pending"
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openTurb,
		[]string{"原测量值"}, []string{"逐项判定"})
}

// 待判定后直接作废的样品没有逐项判定不要求补判定，但原测量值仍是核对历史，
// 丢失同样拒绝。
func TestOpenVoidedFromPendingMeasurementValueMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": null}],
      "status": "voided", "voidReason": "录入信息有误"
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openPH,
		[]string{"原测量值"}, []string{"逐项判定"})
}

// 已确认后作废的样品缺原测量值：作废记录仍是供核对的历史，不能放过丢失的数值。
func TestOpenVoidedAfterConfirmedMeasurementValueMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": null}],
      "status": "voided", "voidReason": "复测确认样品污染", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openPH,
		[]string{"原测量值"}, []string{"逐项判定"})
}

// 逐项判定的 value 字段缺失：原测量明确保存着零、上限为 4、标记为达标，按读出
// 的零与上限比较恰好得到已有的达标标记，但缺字段不能被当成零接受。
func TestOpenConfirmedResultValueFieldMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openPH,
		[]string{"逐项判定", "测量值"}, []string{"上限"})
}

// 逐项判定的 value 保存为 null：与字段缺失同样拒绝。
func TestOpenConfirmedResultValueNull(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": null, "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openPH,
		[]string{"逐项判定", "测量值"}, []string{"上限"})
}

// 判定值缺失却与原测量明确保存的零“一致”：两处一个明确零、一个缺字段，
// 不能拿缺字段去对零。
func TestOpenResultValueMissingDoesNotMatchExplicitZero(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "limit": 0, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openPH,
		[]string{"逐项判定", "测量值"}, nil)
}

// 原测量与逐项判定的 value 同时缺失：两处都读出零也不能互相证明，整次失败。
func TestOpenBothValuesMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH"}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("两处测量值同时缺失必须让整次 Open 失败")
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
}

// 多项目样品只缺浊度的原测量值：不能只接收 pH，整次失败并点名浊度。
func TestOpenMultiItemOneMeasurementValueMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}, {"item": "浊度", "value": null}],
      "status": "pending"
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openTurb,
		[]string{"原测量值"}, nil)
}

// 多项目样品只缺浊度判定的测量值：同样整份拒绝并点名浊度。
func TestOpenMultiItemOneResultValueMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}, {"item": "浊度", "value": 4}],
      "status": "confirmed", "exceeded": true,
      "results": [
        {"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true},
        {"item": "浊度", "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}
      ]
    }`
	writeRawDiskFile(t, dir, rawSample("S1", raw))
	assertOpenRejectsValue(t, dir, "S1", openTurb,
		[]string{"逐项判定", "测量值"}, nil)
}

// 同一文件里另一样品完整正常，也不能跳过缺值样品继续打开：整次失败、无可用
// 存放，完整样品也读不到，原文件保持原样（assertOpenRejects 已核对字节不变）。
func TestOpenMissingValueRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S-GOOD": {
      "id": "S-GOOD", "pointId": "P1",
      "sampledAt": "` + at(5, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    },
    "S-BAD": {
      "id": "S-BAD", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH"}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsValue(t, dir, "S-BAD", openPH,
		[]string{"原测量值"}, nil)
}

// 原测量与逐项判定都明确写出数值零（且零等于零、达标）的记录必须正常读入，
// 零值原样保留；字段存在才是关键，零本身从不被当成缺项。
func TestOpenExplicitZeroValuesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limit": 0, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("原测量与判定都明确为零的完整记录应正常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("完整零值记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	if latest.Measurements[0].Value != 0 || latest.Results[0].Value != 0 ||
		latest.Results[0].Limit != 0 || latest.Exceeded {
		t.Fatalf("零测量值、零上限与达标结论必须原样保留: %+v", latest)
	}
}

// 打开失败不得改写原文件（这里独立核对一次字节，并确认文件中仍保留 null 原值）。
func TestOpenMissingValueLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": null}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	if _, err := Open(dir); err == nil {
		t.Fatalf("缺原测量值必须让整次 Open 失败")
	}
	after, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file after open: %v", err)
	}
	if string(after) != raw {
		t.Fatalf("打开失败不得改写原文件：不填零、不补值")
	}
	if !strings.Contains(string(after), `"value": null`) {
		t.Fatalf("原文件中的 null 必须保留，实际 %s", string(after))
	}
}
