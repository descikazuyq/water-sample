package water

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对逐项判定上限数值的完整性核对：
// 已确认样品，以及已确认后作废、逐项判定仍保留的样品，每条判定都必须实际保存
// 上限数值。limit 字段缺失或保存为 null 都是判定依据残缺，整次 Open 失败、
// 返回 nil 存放、错误可识别为 ErrCorruptRecord 并点名样品编号与项目；不能因
// 读出的零值与测量值、超标标记恰好对得上就当成“零等于零”的合法结论，不能
// 补成零、不用登记的限值填上、不重新判定、不改写原文件。明确保存数值零的
// 上限仍是合法依据；多项目样品只有一项缺上限同样整份拒绝；同文件其他样品
// 正常也不能跳过问题记录继续打开。

// writeRawDiskFile 直接写入原始 JSON 数据文件，用于构造 Go 结构体序列化
// 无法产生的记录（limit 字段缺失或保存为 null）。
func writeRawDiskFile(t *testing.T, dir, raw string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "water-data.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
}

// 判定依据里 limit 字段整个缺失：原测量与判定测量值都是零、单项与整份标记都是
// 达标，内容互相对应，但上限没有保存，不能当成“零等于零”的合法结论。
func TestOpenConfirmedMissingLimitField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// limit 保存为 null 与字段缺失一样是判定依据残缺：null 不是数值零。
func TestOpenConfirmedNullLimit(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limit": null, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 多项目样品只有浊度一项缺少上限：不能仅接收 pH 的判定或保留整份结论，
// 整次打开失败并点名缺上限的项目。
func TestOpenMultiItemOneMissingLimit(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}, {"item": "浊度", "value": 4}],
      "status": "confirmed", "exceeded": true,
      "results": [
        {"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true},
        {"item": "浊度", "value": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}
      ]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejects(t, dir, "S1", openTurb, false)
}

// 已确认后作废、逐项判定仍保留的样品，判定缺少上限同样整次拒绝：
// 作废只取消有效资格，不能让缺依据的结论借“历史记录”名义混入台账。
func TestOpenVoidedAfterConfirmedMissingLimit(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "voided", "voidReason": "复测确认样品污染", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 即使文件里登记着采样当时适用的限值（9 > 8 本可判超标），也不能据此补造
// 已保存结论的依据：缺少上限数值的判定仍整次拒绝，不填值、不重新判定。
func TestOpenMissingLimitNotBackfilledFromRegisteredLimit(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "value": 8, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejects(t, dir, "S1", openPH, false)
}

// 同一文件中其他样品完整正常，也不能跳过缺上限的记录继续打开：
// 整次失败、无可用存放，正常样品也读不到。
func TestOpenMissingLimitRejectsWholeFile(t *testing.T) {
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
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejects(t, dir, "S-BAD", openPH, false)
}

// 明确保存数值零的上限是合法依据：测量值为零、上限为零、单项与整份都达标
// 的记录必须正常读入，上限零原样保留，并可作为最近有效结果返回。
func TestOpenExplicitZeroLimitIsValid(t *testing.T) {
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
		t.Fatalf("明确保存的零上限是合法依据，应正常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("完整记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Value != 0 || r.Limit != 0 || r.Exceeded || latest.Exceeded {
		t.Fatalf("零上限与达标结论必须原样保留: %+v exceeded=%v", r, latest.Exceeded)
	}
}

// 上限数值正常保存的记录不因这次核对受影响：录入、确认、作废、重开后的
// 查询入口与保存格式保持原样。
func TestOpenSavedLimitRoundTripUnchanged(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8.0, openEff())
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	conf, err := s.Confirm("S1")
	if err != nil || !conf.Exceeded || conf.Results[0].Limit != 8.0 {
		t.Fatalf("setup: 9 > 8 应超标: %+v err=%v", conf, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常保存的记录重新打开不应失败: %v", err)
	}
	defer s2.Close()
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	r := latest.Results[0]
	if r.Value != 9 || r.Limit != 8.0 || !r.LimitEffective.Equal(openEff()) || !r.Exceeded {
		t.Fatalf("已保存的判定依据必须原样保留: %+v", r)
	}
}
