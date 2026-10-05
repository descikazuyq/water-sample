package water

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对测量值字段的完整性核对：每个原测量项目的
// value，以及已保存的每条逐项判定中的 value，字段缺失或保存为 null 都按
// 损坏记录处理——JSON 反序列化会把它们落成零，与明确保存的零无法区分，
// 只能按字段是否存在核对。整次 Open 失败、返回 nil 存放、错误可识别为
// ErrCorruptRecord 并点名样品编号与项目、说明缺少的是原测量值还是逐项判定
// 中的测量值；不能因读出的零与另一处明确保存的零一致、或按零与上限比较
// 能得到已保存的达标标记就接受；不填零、不从同项目的另一处记录补值、
// 不重新计算结论、不改写原文件。明确保存的数值零仍是合法测量值。

// assertOpenRejectsMissingValue 在 assertOpenRejects 的基础上进一步要求
// 错误信息说明缺少的是哪一处测量值（origin 为“原测量值”或“逐项判定”）。
func assertOpenRejectsMissingValue(t *testing.T, dir, sampleID, item, origin string) {
	t.Helper()
	assertOpenRejects(t, dir, sampleID, item, false)
	// assertOpenRejects 已重开过文件并断言失败；这里再打开一次核对错误措辞。
	_, err := Open(dir)
	if err == nil {
		t.Fatalf("损坏数据必须让整次 Open 失败，sample=%s", sampleID)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, origin) {
		t.Fatalf("错误信息应说明缺少的是%s，实际 %q", origin, msg)
	}
	if !strings.Contains(msg, "value") {
		t.Fatalf("错误信息应指出 value 字段，实际 %q", msg)
	}
}

// 待判定样品的原测量 value 字段整个缺失：读出的零不是合法测量值，
// 不能让它随后凭这个零完成确认，整次打开失败并点名样品与项目。
func TestOpenPendingMissingMeasurementValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH"}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openPH, "原测量值")
}

// 原测量 value 保存为 null 与字段缺失一样是内容残缺：null 不是数值零。
func TestOpenPendingNullMeasurementValue(t *testing.T) {
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
	assertOpenRejectsMissingValue(t, dir, "S1", openPH, "原测量值")
}

// 多项目待判定样品只有浊度一项缺测量值：不能只接收 pH 一项，整次打开失败。
func TestOpenPendingMultiItemOneMissingValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}, {"item": "浊度"}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openTurb, "原测量值")
}

// 待判定后直接作废、没有逐项判定的样品，原测量缺测量值同样不能放过：
// 作废记录仍是供核对的历史。
func TestOpenVoidedFromPendingMissingMeasurementValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": null}],
      "status": "voided", "voidReason": "录入信息有误"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openPH, "原测量值")
}

// 已确认样品的原测量缺测量值，逐项判定里明确保存了零：缺失后恰好与另一处
// 明确保存的零一致也不能成为接受理由，更不能从判定补值。
func TestOpenConfirmedMissingMeasurementValueResultHasZero(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH"}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": 0, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openPH, "原测量值")
}

// 已确认样品的逐项判定缺测量值，原测量明确保存了零、按零与上限比较也能
// 得到已保存的达标标记：两处都不能成为接受理由，整次打开失败。
func TestOpenConfirmedMissingResultValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openPH, "逐项判定")
}

// 逐项判定的 value 保存为 null 与字段缺失一样是判定内容残缺。
func TestOpenConfirmedNullResultValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "value": null, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openPH, "逐项判定")
}

// 已确认后作废、逐项判定仍保留的样品，判定缺测量值同样整次拒绝：
// 作废只取消有效资格，不能让缺数值的结论借“历史记录”名义混入台账。
func TestOpenVoidedAfterConfirmedMissingResultValue(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 9}],
      "status": "voided", "voidReason": "复测确认样品污染", "exceeded": true,
      "results": [{"item": "pH", "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": true}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openPH, "逐项判定")
}

// 原测量与逐项判定同时缺测量值：两处都缺也不能互相印证，整次打开失败。
func TestOpenConfirmedBothValuesMissing(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH"}],
      "status": "confirmed", "exceeded": false,
      "results": [{"item": "pH", "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openPH, "原测量值")
}

// 多项目已确认样品只有浊度一项的判定缺测量值：不能只接收 pH 的判定，
// 整次打开失败并点名缺数值的项目。
func TestOpenConfirmedMultiItemOneResultValueMissing(t *testing.T) {
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
        {"item": "浊度", "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}
      ]
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	assertOpenRejectsMissingValue(t, dir, "S1", openTurb, "逐项判定")
}

// 一份缺测量值的样品与其他完整样品同处一个文件：不能只跳过它继续打开，
// 整次失败、无可用存放，完整样品也读不到；原文件保持原样。
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
	assertOpenRejectsMissingValue(t, dir, "S-BAD", openPH, "原测量值")
	if _, err := Open(dir); !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("完整样品也不能让整次打开通过，got %v", err)
	}
}

// 明确写出数值零的原测量与判定都是合法记录：内容完整且两处都为零的样品
// 必须正常读入，零值原样保留，结论不重算。
func TestOpenExplicitZeroValuesAccepted(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "P1",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}, {"item": "浊度", "value": 0}],
      "status": "confirmed", "exceeded": false,
      "results": [
        {"item": "pH", "value": 0, "limit": 8, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false},
        {"item": "浊度", "value": 0, "limit": 4, "limitEffective": "` +
		openEff().Format(time.RFC3339Nano) + `", "exceeded": false}
      ]
    },
    "S2": {
      "id": "S2", "pointId": "P1",
      "sampledAt": "` + at(11, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 0}],
      "status": "pending"
    }
  }
}`
	writeRawDiskFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("明确保存的零是合法测量值，完整记录应正常读入: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	for _, r := range latest.Results {
		if r.Value != 0 {
			t.Fatalf("明确保存的零判定值必须原样保留: %+v", latest.Results)
		}
	}
}

// 打开失败后原文件字节必须保持原样：不填零、不补值、不回写。
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
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file before open: %v", err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("缺测量值的记录必须让整次 Open 失败，got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read file after open: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("打开失败不得覆盖或改写原数据文件")
	}
}
