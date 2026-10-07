package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对已保存超标标记的存在性核对：
// 已确认样品，以及已作废但仍保留逐项判定的样品，正常确认会把整份与每条逐项的
// exceeded 明确保存为布尔值；字段缺失或保存为 null 都是结论残缺，不能因为
// JSON 反序列化后的 false 恰好与按保存依据（测量值、上限、生效时间）比较得到
// 的达标结论一致，就把缺少结论当成已有的达标结论。整份标记缺失、逐项标记缺失
// （多项目样品只缺一项同样）、字段保存为 null，都必须让整次 Open 失败：
// 返回 nil 存放、错误可被 errors.Is(err, ErrCorruptRecord) 识别，信息点名样品
// 编号并区分缺少的是整份超标标记还是逐项超标标记（逐项时点名项目），原文件
// 字节不变，同文件其他样品正常也不部分读入。不按测量值重新生成标记、不用逐项
// 结果补整份标记、不改变状态或历史依据。明确保存的 false 与 true 仍是已保存
// 标记，照常按依据核对；待判定样品、从未确认就作废且没有逐项判定的样品不要求
// 提前保存结论，继续按原规则读取。

// assertOpenRejectsExceeded 断言缺少超标标记让整次打开失败：nil 存放、
// ErrCorruptRecord、信息点名样品编号，并按 wholeFlag 区分应说明缺少整份标记
// 还是缺少 item 指定项目的逐项标记；原文件字节不变。
func assertOpenRejectsExceeded(t *testing.T, dir, sampleID, item string, wholeFlag bool) {
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
		t.Fatalf("缺少超标标记必须让整次 Open 失败，sample=%s", sampleID)
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
	if wholeFlag {
		if !strings.Contains(msg, "整份超标标记") {
			t.Fatalf("错误信息应说明缺少整份超标标记，实际 %q", msg)
		}
	} else {
		want := "逐项超标标记"
		if !strings.Contains(msg, want) || !strings.Contains(msg, item) {
			t.Fatalf("错误信息应指出项目 %q 缺少%s，实际 %q", item, want, msg)
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

// 题面示例：pH 测量值 7、保存上限 8，逐项标记明确为 false，但整份标记被删掉。
// 按依据比较本应达标，仍必须整次失败并说明缺少整份超标标记。
func TestOpenConfirmedMissingWholeExceededField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed",
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", "", true)
}

// 整份标记保存为 null 与字段缺失一样是结论残缺：null 不是已保存的 false。
func TestOpenConfirmedNullWholeExceeded(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": null,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", "", true)
}

// 即使按测量值与上限重算必然超标（9 > 8），整份标记缺失也不能接受：
// 不根据测量值重新生成标记。
func TestOpenMissingWholeExceededNotRecomputedWhenExceeding(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed",
      "results": [{"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", "", true)
}

// 整份标记存在（false），但这条逐项判定的超标标记丢失：反过来的残缺同样拒绝，
// 并点名项目。
func TestOpenConfirmedMissingItemExceededField(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", openPH, false)
}

// 逐项超标标记保存为 null：与字段缺失同样拒绝。
func TestOpenConfirmedNullItemExceeded(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": null}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", openPH, false)
}

// 多项目样品只缺一个项目的逐项标记：属于结论残缺，点名缺失的浊度项目；
// 即使另一项（pH）标记完整、整份标记也完整，也不能放行。
func TestOpenMultiItemOneMissingItemExceeded(t *testing.T) {
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
        {"item": "浊度", "value": 4, "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `"}
      ]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", openTurb, false)
}

// 多项目样品的逐项标记都完整，整份标记被删掉：同样拒绝，不能用逐项结果补出
// 整份标记。
func TestOpenMultiItemMissingWholeExceeded(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}, {"item": "浊度", "value": 4}],
      "status": "confirmed",
      "results": [
        {"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false},
        {"item": "浊度", "value": 4, "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}
      ]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", "", true)
}

// 已确认后作废、逐项判定仍保留的样品缺少整份标记：作废只取消有效资格，
// 残缺结论不能借“历史记录”名义混入。
func TestOpenVoidedAfterConfirmedMissingWholeExceeded(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "voided", "voidReason": "复测确认样品污染",
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", "", true)
}

// 已确认后作废、逐项判定仍保留的样品缺少某条逐项标记：同样整次拒绝。
func TestOpenVoidedAfterConfirmedMissingItemExceeded(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "voided", "voidReason": "复测确认样品污染", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S1", openPH, false)
}

// 同一文件中其他样品完整正常，也不能只读入正常部分：整次失败、无可用存放。
func TestOpenMissingExceededRejectsWholeFile(t *testing.T) {
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
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed",
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsExceeded(t, dir, "S-BAD", "", true)
}

// 待判定样品不要求提前保存超标标记：整份与逐项（本就没有判定）标记都缺失时
// 照常读入，读入后仍是待判定、没有结论。
func TestOpenPendingMissingExceededFieldsOK(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("待判定样品不要求提前保存超标标记: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	smp := list[0]
	if smp.Status != StatusPending || smp.Results != nil || smp.Exceeded {
		t.Fatalf("待判定样品读入后不应带结论: %+v", smp)
	}
}

// 从未确认就作废且没有逐项判定的样品同样不要求超标标记，照常读入。
func TestOpenVoidedFromPendingMissingExceededFieldsOK(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusVoided, VoidReason: "录入信息有误",
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("未确认就作废、无逐项判定的样品不要求超标标记: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	smp := list[0]
	if smp.Status != StatusVoided || len(smp.Results) != 0 || smp.Exceeded {
		t.Fatalf("该作废样品不应凭空获得结论: %+v", smp)
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok {
		t.Fatalf("作废样品不参与最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 明确保存的 false 是已保存标记：依据齐全且一致时正常读入，false 原样保留，
// 按点查询、最近有效结果与重复确认都返回已保存内容。
func TestOpenExplicitFalseExceededPreserved(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 7, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("明确保存的 false 是已保存标记，应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByPoint: %+v err=%v", list, err)
	}
	if got := list[0]; got.Exceeded || len(got.Results) != 1 || got.Results[0].Exceeded {
		t.Fatalf("明确保存的 false 必须原样保留: %+v", got)
	}
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("正常记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	if latest.Exceeded || latest.Results[0].Exceeded {
		t.Fatalf("最近有效结果应返回保存的达标结论: %+v", latest)
	}
	// 重复确认返回同一份已保存内容。
	again, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("重复确认不应失败: %v", err)
	}
	if again.Exceeded || again.Results[0].Exceeded || again.Results[0].Limit != 8 {
		t.Fatalf("重复确认应返回已保存内容: %+v", again)
	}
}

// 正常录入、确认、作废流程保存的数据本来就明确写有超标标记，重新打开不受这次
// 核对影响；作废记录仍只供核对历史，不参与最近有效结果。
func TestOpenNormalFlowExceededRoundTrip(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8.0, openEff())
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	conf, err := s.Confirm("S1")
	if err != nil || !conf.Exceeded || !conf.Results[0].Exceeded {
		t.Fatalf("setup: 9 > 8 应超标: %+v err=%v", conf, err)
	}
	if _, err := s.Void("S1", "复测确认样品污染"); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常流程保存的记录重新打开不应失败: %v", err)
	}
	defer s2.Close()
	list, err := s2.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("作废历史记录仍应可按点核对: %+v err=%v", list, err)
	}
	if got := list[0]; got.Status != StatusVoided || !got.Exceeded ||
		len(got.Results) != 1 || !got.Results[0].Exceeded {
		t.Fatalf("历史超标标记必须原样保留: %+v", got)
	}
	if latest, ok, err := s2.LatestResult("P1"); err != nil || ok {
		t.Fatalf("作废记录不参与最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 明确保存为 true 的标记与明确保存的 false 一样是已保存标记：缺失检查放过，
// 随后的依据一致性核对照常工作（这里依据一致，正常读入）。
func TestOpenExplicitTrueExceededAccepted(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "confirmed", "exceeded": true,
      "results": [{"item": "pH", "value": 9, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("明确保存的 true 是已保存标记且与依据一致，应正常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("正常记录应可作为最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	if !latest.Exceeded || !latest.Results[0].Exceeded {
		t.Fatalf("保存的超标结论必须原样保留: %+v", latest)
	}
}
