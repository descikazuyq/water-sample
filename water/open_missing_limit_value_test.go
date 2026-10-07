package water

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对登记限值上限数值的完整性核对：
// 文件中实际存在的每一版登记限值都必须实际保存上限数值，value 字段缺失或
// 保存为 null 都是限值记录残缺——JSON 反序列化会把它们落成零，与明确保存的
// 零上限无法区分，只能按字段是否存在核对。整次 Open 失败、返回 nil 存放、
// 错误可识别为 ErrCorruptRecord 并点到该限值自身的采样点编号、项目与生效
// 时间，明确说明缺少上限数值；不补成零、不跳过这一版、不借用同项目另一版
// 的数值、不改写原文件，也不把拒绝推迟到确认某份样品时。这条核对与当前有
// 没有样品、这一版是否已被样品采用无关：旧版或尚未生效的版本缺少数值同样
// 拒绝，文件尚未录入任何样品时也一样。明确保存的数值零仍是合法上限，负数
// 和正数保持现有含义；没有登记限值的空数据照常打开。

// assertOpenRejectsLimitValue 断言打开因某版登记限值缺少上限数值而整次失败：
// 返回 nil 存放、错误可识别为 ErrCorruptRecord，信息点到该限值自身的采样点
// 编号、项目与生效时间并说明缺少上限数值，原文件字节不被改动。
func assertOpenRejectsLimitValue(t *testing.T, dir, point, item string, eff time.Time) {
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
		t.Fatalf("缺少上限数值的限值必须让整次 Open 失败，%s/%s @ %s", point, item, eff)
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{point, item, eff.UTC().String(), "上限数值", "value"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应包含 %q（采样点、项目、生效时间与缺少上限数值的说明），实际 %q", want, msg)
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

// 唯一一版限值的 value 字段整个缺失：读出的零不是合法上限，不能让它随后被
// 当成零上限参与判定，整次打开失败并点名该限值的采样点、项目与生效时间。
func TestOpenLimitMissingValueField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitValue(t, dir, "P1", openPH, openEff())
}

// 限值的 value 保存为 null 与字段缺失一样是记录残缺：null 不是数值零。
func TestOpenLimitNullValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "value": null, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitValue(t, dir, "P1", openPH, openEff())
}

// 文件尚未录入任何样品时，缺少数值的限值同样让整次打开失败：
// 这条核对与有没有样品无关，不能等确认某份样品时才报告。
func TestOpenLimitMissingValueWithoutSamples(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]}
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitValue(t, dir, "P1", openPH, openEff())
}

// 同一项目有多版限值，旧版缺少数值、最新版完整：不能只检查最新版本，
// 缺数值的旧版同样让整次打开失败，也不能借用另一版的数值补上。
func TestOpenOldLimitVersionMissingValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [
    {"pointId": "P1", "item": "pH", "effective": "` + at(1, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "pH", "value": 8, "effective": "` + at(5, 0).Format(time.RFC3339Nano) + `"}
  ]},
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
	assertOpenRejectsLimitValue(t, dir, "P1", openPH, at(1, 0))
}

// 尚未生效的版本缺少数值同样拒绝：它还没被任何样品采用，也不能跳过。
func TestOpenFutureLimitVersionMissingValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [
    {"pointId": "P1", "item": "pH", "value": 8, "effective": "` + at(1, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "pH", "effective": "` + at(20, 0).Format(time.RFC3339Nano) + `"}
  ]},
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
	assertOpenRejectsLimitValue(t, dir, "P1", openPH, at(20, 0))
}

// 缺数值的限值与其他采样点、其他项目的完整限值以及完整样品同处一个文件：
// 不能只跳过问题版本继续打开，整次失败、无可用存放，其余数据也读不到。
func TestOpenLimitMissingValueRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {
    "P1": {"id": "P1", "name": "一号取水口"},
    "P2": {"id": "P2", "name": "二号取水口"}
  },
  "limits": {
    "P1-pH": [{"pointId": "P1", "item": "pH", "effective": "` + openEff().Format(time.RFC3339Nano) + `"}],
    "P2-pH": [{"pointId": "P2", "item": "pH", "value": 8, "effective": "` + openEff().Format(time.RFC3339Nano) + `"}],
    "P1-COD": [{"pointId": "P1", "item": "COD", "value": 30, "effective": "` + openEff().Format(time.RFC3339Nano) + `"}]
  },
  "samples": {
    "S-GOOD": {
      "id": "S-GOOD", "pointId": "P2",
      "sampledAt": "` + at(5, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitValue(t, dir, "P1", openPH, openEff())
	if _, err := Open(dir); !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("其他采样点、限值与样品再正常也不能让整次打开通过，got %v", err)
	}
}

// 明确保存数值零的限值是合法上限：文件正常打开，测量值为零达标、大于零超标。
func TestOpenExplicitZeroLimitValueAccepted(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "value": 0, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]},
  "samples": {
    "S-ZERO": {
      "id": "S-ZERO", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "pending"
    },
    "S-OVER": {
      "id": "S-OVER", "pointId": "P1",
      "sampledAt": "` + at(11, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 1}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("明确保存的零上限是合法限值，应正常打开: %v", err)
	}
	defer s.Close()
	zero, err := s.Confirm("S-ZERO")
	if err != nil {
		t.Fatalf("Confirm S-ZERO: %v", err)
	}
	if r := zero.Results[0]; r.Limit != 0 || r.Exceeded || zero.Exceeded {
		t.Fatalf("适用上限为零时测量值为零应达标: %+v exceeded=%v", r, zero.Exceeded)
	}
	over, err := s.Confirm("S-OVER")
	if err != nil {
		t.Fatalf("Confirm S-OVER: %v", err)
	}
	if r := over.Results[0]; r.Limit != 0 || !r.Exceeded || !over.Exceeded {
		t.Fatalf("适用上限为零时测量值大于零应超标: %+v exceeded=%v", r, over.Exceeded)
	}
}

// 负数与正数限值保持现有含义：明确保存的负上限与正上限都正常读入并参与判定。
func TestOpenNegativeAndPositiveLimitValuesAccepted(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {
    "P1-pH": [{"pointId": "P1", "item": "pH", "value": -1, "effective": "` + openEff().Format(time.RFC3339Nano) + `"}],
    "P1-COD": [{"pointId": "P1", "item": "COD", "value": 30, "effective": "` + openEff().Format(time.RFC3339Nano) + `"}]
  },
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": -2}, {"item": "COD", "value": 30}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("明确保存的负数与正数限值应正常打开: %v", err)
	}
	defer s.Close()
	smp, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	rs := map[string]ItemResult{}
	for _, r := range smp.Results {
		rs[r.Item] = r
	}
	if ph := rs[openPH]; ph.Limit != -1 || ph.Exceeded {
		t.Fatalf("-2 ≤ -1 应达标: %+v", ph)
	}
	if cod := rs["COD"]; cod.Limit != 30 || cod.Exceeded {
		t.Fatalf("30 ≤ 30 应达标: %+v", cod)
	}
}

// 完整的多版本限值即使保存顺序不同也正常打开，确认时仍按采样时间选取当时
// 已生效的最近一版，恰好等于生效时刻采用新版。
func TestOpenCompleteMultiVersionLimitsUnordered(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [
    {"pointId": "P1", "item": "pH", "value": 6, "effective": "` + at(10, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "pH", "value": 9, "effective": "` + at(1, 0).Format(time.RFC3339Nano) + `"},
    {"pointId": "P1", "item": "pH", "value": 7, "effective": "` + at(5, 0).Format(time.RFC3339Nano) + `"}
  ]},
  "samples": {
    "S-MID": {
      "id": "S-MID", "pointId": "P1",
      "sampledAt": "` + at(6, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 8}],
      "status": "pending"
    },
    "S-AT": {
      "id": "S-AT", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 8}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("完整的乱序多版本限值应正常打开: %v", err)
	}
	defer s.Close()
	mid, err := s.Confirm("S-MID")
	if err != nil {
		t.Fatalf("Confirm S-MID: %v", err)
	}
	if r := mid.Results[0]; r.Limit != 7 || !r.LimitEffective.Equal(at(5, 0)) || !r.Exceeded {
		t.Fatalf("9/6 采样应取 9/5 生效的 7、8 > 7 超标: %+v", r)
	}
	atBoundary, err := s.Confirm("S-AT")
	if err != nil {
		t.Fatalf("Confirm S-AT: %v", err)
	}
	if r := atBoundary.Results[0]; r.Limit != 6 || !r.LimitEffective.Equal(at(10, 0)) || !r.Exceeded {
		t.Fatalf("恰好生效时刻应采用新版 6: %+v", r)
	}
}

// 缺数值的限值与完整的已确认结论、作废记录同处一个文件：整次打开失败，
// 历史依据不因这次读取核对而改选限值或重新判定——打开根本不会成功。
func TestOpenLimitMissingValueWithConfirmedAndVoidedSamples(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]},
  "samples": {
    "SC": {
      "id": "SC", "pointId": "P1",
      "sampledAt": "` + at(5, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    },
    "SV": {
      "id": "SV", "pointId": "P1",
      "sampledAt": "` + at(6, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "voided", "voidReason": "复测确认样品污染", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsLimitValue(t, dir, "P1", openPH, openEff())
}

// 没有登记限值的数据照常打开：空数据、只有采样点或只有样品的数据都不受
// 这条核对影响；后续样品缺少适用上限时仍按原有行为在确认时拒绝。
func TestOpenNoRegisteredLimitsStillOpens(t *testing.T) {
	// 只有采样点、没有任何限值与样品。
	dir := t.TempDir()
	writeRawDiskFile(t, dir, `{"points": {"P1": {"id": "P1", "name": "一号取水口"}}}`)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("没有登记限值的数据应正常打开: %v", err)
	}
	defer s.Close()

	// 录入一份没有适用上限的样品：打开不受影响，确认时仍按原有行为拒绝。
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 7})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("有样品但没有登记限值的数据应正常打开: %v", err)
	}
	defer s2.Close()
	if _, err := s2.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("缺少适用上限仍应在确认时拒绝，got %v", err)
	}
}

// 限值归属沿用现有行为：共用旧版存储键的不同（采样点，项目）组合各自成组，
// 其中一组缺少数值时按该组自身的采样点与项目报告，不误伤另一组。
func TestOpenLimitMissingValueWithinOldCollidedKey(t *testing.T) {
	dir := t.TempDir()
	zeroPoint := "P\x00A"
	zeroItem := "A\x00B"
	oldKey := zeroPoint + "\x00" + "B"
	if oldKey != "P"+"\x00"+zeroItem {
		t.Fatal("测试前提：两个组合应拼出同一个旧键")
	}
	// 完整的一组与缺数值的一组共用同一个旧版存储键；两组各自归属的采样点都已
	// 登记（新核对要求限值先属于已登记采样点），这里只制造缺数值损坏。先经 Go
	// 结构体序列化落盘，再按通用 JSON 结构把第二条的 value 字段删掉，构造出缺少
	// 数值的限值记录（含 U+0000 的文本落盘时被转义，直接拼原始字符串不可靠）。
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P":       {ID: "P", Name: "普通编号"},
			zeroPoint: {ID: zeroPoint, Name: "带零编号"},
		},
		Limits: map[string][]Limit{
			oldKey: {
				{PointID: "P", Item: zeroItem, Value: 5, Effective: openEff()},
				{PointID: zeroPoint, Item: "B", Effective: openEff()},
			},
		},
	})
	data, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal data file: %v", err)
	}
	versions, ok := doc["limits"].(map[string]interface{})[oldKey].([]interface{})
	if !ok || len(versions) != 2 {
		t.Fatalf("测试前提：应有两版限值记录在同一旧键下: %s", data)
	}
	second, ok := versions[1].(map[string]interface{})
	if !ok || second["pointId"] != zeroPoint || second["item"] != "B" {
		t.Fatalf("测试前提：第二版应是 %q/B: %s", zeroPoint, data)
	}
	delete(second, "value")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("remarshal data file: %v", err)
	}
	writeRawDiskFile(t, dir, string(raw))
	assertOpenRejectsLimitValue(t, dir, zeroPoint, "B", openEff())
}
