package water

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对登记限值采样点归属的核对：每一版限值记录自身
// 保存的 pointId（不是 limits 的集合键）必须完整（字段缺失、为 null、为空
// 字符串或仅含空白都算缺少采样点编号），不含首尾空白，且按 JSON 解码后的
// 文本与本文件已登记的采样点编号逐字匹配（不转大小写；Unicode 转义表示同一
// 文本可以匹配；编号内部含 U+0000 的合法记录仍可读取）。编号完整但没有对应
// 登记记录时，必须明确报该采样点未登记，不能凭限值或样品反过来补登记。
// 核对针对文件中实际存在的每一版：即使尚无样品，或只有旧版、未来版本归属有
// 问题，也让整次 Open 失败、返回 nil 存放、错误可识别为 ErrCorruptRecord；
// 信息指出这是登记限值的归属问题与对应测量项目（带出生效时间），区分三种
// 问题，含首尾空白与未登记时带出原编号。其他限值与样品正常也不能只读入正常
// 部分，原文件字节不变。样品和限值同时引用未登记采样点时，仍沿用样品归属
// 错误（保留样品编号与未登记说明）。

// limitOwnershipPoint 只登记 P1 时，构造一版数值与生效时间完整、但归属给定
// pointID 的 pH 上限。
func limitOwnershipVersions(pointID string, effs ...time.Time) []Limit {
	versions := make([]Limit, 0, len(effs))
	for _, eff := range effs {
		versions = append(versions, Limit{PointID: pointID, Item: openPH, Value: 8, Effective: eff})
	}
	return versions
}

// assertOpenLimitOwnershipCorrupt 打开并断言：整次失败、nil 存放、
// ErrCorruptRecord，去掉哨兵前缀后的具体原因包含所有 want 子串、且不包含任一
// notWant 子串，原文件字节不变。
func assertOpenLimitOwnershipCorrupt(t *testing.T, dir string, want, notWant []string) {
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
		t.Fatalf("归属有问题的限值必须让整次 Open 失败")
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	detail := strings.TrimPrefix(err.Error(), ErrCorruptRecord.Error())
	for _, w := range want {
		if !strings.Contains(detail, w) {
			t.Fatalf("具体错误原因应包含 %q，实际 %q", w, detail)
		}
	}
	for _, nw := range notWant {
		if strings.Contains(detail, nw) {
			t.Fatalf("具体错误原因不应包含 %q，实际 %q", nw, detail)
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

// 任务中的核心场景：文件只登记了 P1，limits 里却保存一版归属 P9 的 pH 上限，
// 数值与生效时间完整也必须整次拒绝；否则后来登记 P9 并确认样品会用上这条
// 原本无法通过正常登记入口写入的限值。核对的是记录自身的 pointId，不是它
// 所在集合的键（这里故意放在 P1-pH 键下）。
func TestOpenLimitBelongsToUnregisteredPoint(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): limitOwnershipVersions("P9", openEff()),
		},
	})
	assertOpenLimitOwnershipCorrupt(t, dir,
		[]string{"登记限值", openPH, openEff().UTC().String(), strconv.Quote("P9"), "未登记"},
		[]string{"缺少采样点编号", "含首尾空白"})

	// 打开失败返回 nil 存放：归属不明的限值不可能进入任何后续确认。
	if got, err := Open(dir); err == nil {
		got.Close()
		t.Fatalf("归属不明的限值仍在文件中时打开必须持续失败")
	}
}

// 归属核对只认记录自身的 pointId，不认 limits 集合键：记录归属已登记的 P1、
// 却放在 P9-pH 键下时照常读入，确认样品仍能用上这版上限。
func TestOpenLimitOwnershipUsesRecordIDNotCollectionKey(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P9", openPH): limitOwnershipVersions("P1", openEff()),
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("归属看记录自身 pointId（P1 已登记），与集合键无关: %v", err)
	}
	defer s.Close()
	smp, err := s.Confirm("S1")
	if err != nil {
		t.Fatalf("集合键叫 P9 不影响按记录自身归属使用 P1 的限值: %v", err)
	}
	if r := smp.Results[0]; r.Limit != 8 || r.Exceeded {
		t.Fatalf("应使用记录自身归属 P1 的限值 8、7 ≤ 8 达标: %+v", r)
	}
}

// 这条核对与当前有没有样品无关：文件尚未录入任何样品时，归属未登记点的限值
// 同样让整次打开失败。
func TestOpenUnregisteredLimitWithoutSamples(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P9", openPH): limitOwnershipVersions("P9", openEff()),
		},
	})
	assertOpenLimitOwnershipCorrupt(t, dir,
		[]string{"登记限值", openPH, strconv.Quote("P9"), "未登记"}, nil)
}

// 只有旧版归属未登记点、最新版归属已登记点，也不能只检查最新版本：旧版同样
// 让整次打开失败。
func TestOpenOldLimitVersionBelongsToUnregisteredPoint(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P9", Item: openPH, Value: 9, Effective: at(1, 0)},
				{PointID: "P1", Item: openPH, Value: 8, Effective: at(5, 0)},
			},
		},
	})
	assertOpenLimitOwnershipCorrupt(t, dir,
		[]string{"登记限值", openPH, at(1, 0).UTC().String(), strconv.Quote("P9"), "未登记"}, nil)
}

// 尚未生效的未来版本归属未登记点同样拒绝：它还没被任何样品采用，也不能跳过。
func TestOpenFutureLimitVersionBelongsToUnregisteredPoint(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: at(1, 0)},
				{PointID: "P9", Item: openPH, Value: 7, Effective: at(20, 0)},
			},
		},
	})
	assertOpenLimitOwnershipCorrupt(t, dir,
		[]string{"登记限值", openPH, at(20, 0).UTC().String(), strconv.Quote("P9"), "未登记"}, nil)
}

// pointId 字段的各种缺失写法都算缺少采样点编号：字段缺失、null 用原始 JSON
// 构造；空字符串、仅含空白用结构体直接构造。
func TestOpenLimitMissingPointIDForms(t *testing.T) {
	cases := []struct {
		name    string
		raw     bool
		token   string // raw=true 时这一版限值记录里 pointId 字段的原始 JSON 片段，空串表示字段缺失
		pointID string // raw=false 时结构体直接使用的 pointId
	}{
		{"字段缺失", true, "", ""},
		{"null", true, `"pointId":null,`, ""},
		{"空字符串", false, "", ""},
		{"仅含空格", false, "", "   "},
		{"仅含制表与换行", false, "", "\t\n "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.raw {
				pointField := c.token
				raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{` + pointField + `"item": "pH", "value": 8, "effective": "` +
					openEff().Format(time.RFC3339Nano) + `"}]}
}`
				writeRawDiskFile(t, dir, raw)
			} else {
				writeDiskFile(t, dir, diskState{
					Points: openPoints(),
					Limits: map[string][]Limit{
						diskLimitKey("P1", openPH): limitOwnershipVersions(c.pointID, openEff()),
					},
				})
			}
			assertOpenLimitOwnershipCorrupt(t, dir,
				[]string{"登记限值", openPH, openEff().UTC().String(), "缺少采样点编号"},
				[]string{"未登记", "含首尾空白"})
		})
	}
}

// 带首尾空白的编号属于损坏：即使去掉空白后正好是已登记的 P1，也不能替它
// 匹配采样点；错误信息带出原编号。
func TestOpenLimitPointIDWithSurroundingWhitespaceRejected(t *testing.T) {
	cases := []struct {
		name    string
		pointID string
	}{
		{"首尾空格", " P1 "},
		{"仅前导制表", "\tP1"},
		{"仅末尾换行", "P1\n"},
		{"空白但去空白后非空", "  P1  "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Limits: map[string][]Limit{
					diskLimitKey("P1", openPH): limitOwnershipVersions(c.pointID, openEff()),
				},
			})
			assertOpenLimitOwnershipCorrupt(t, dir,
				[]string{"登记限值", openPH, "含首尾空白", strconv.Quote(c.pointID)},
				[]string{"未登记", "缺少采样点编号"})
		})
	}
}

// 编号按 JSON 解码后的文本逐字匹配、不转换大小写：P1 已登记，限值写 p1 时
// 按采样点未登记拒绝，错误带出原编号 p1。
func TestOpenLimitPointIDComparisonIsCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): limitOwnershipVersions("p1", openEff()),
		},
	})
	assertOpenLimitOwnershipCorrupt(t, dir,
		[]string{"登记限值", openPH, strconv.Quote("p1"), "未登记"},
		[]string{"含首尾空白", "缺少采样点编号"})
}

// Unicode 转义只是 JSON 的另一种写法：pointId 用 P1 表示 P1，解码后与
// 已登记编号是同一文本，限值照常读入并能被确认使用；中文编号同理。
func TestOpenLimitPointIDUnicodeEscapeSameTextAccepted(t *testing.T) {
	t.Run("P1转义", func(t *testing.T) {
		dir := t.TempDir()
		// 文件里真的出现 \u00501 这样的六个转义字符（P = U+0050），JSON 解码后是
		// P1，与已登记编号是同一文本；集合键故意不写成 P1-pH。
		raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"x-pH": [{"pointId": "\u00501", "item": "pH", "value": 8, "effective": "` +
			openEff().Format(time.RFC3339Nano) + `"}]},
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
			t.Fatalf("Unicode 转义表示同一已登记编号时限值应正常读入: %v", err)
		}
		defer s.Close()
		smp, err := s.Confirm("S1")
		if err != nil {
			t.Fatalf("转义写法的归属应与 P1 匹配并参与确认: %v", err)
		}
		if r := smp.Results[0]; r.Limit != 8 {
			t.Fatalf("应使用转义归属 P1 的限值 8: %+v", r)
		}
	})

	t.Run("中文编号转义", func(t *testing.T) {
		dir := t.TempDir()
		pts := map[string]SamplingPoint{
			"一号": {ID: "一号", Name: "中文采样点"},
		}
		ptsJSON, err := json.Marshal(pts)
		if err != nil {
			t.Fatalf("marshal points: %v", err)
		}
		// 一 = U+4E00，号 = U+53F7：限值 pointId 用转义写法，解码后与已登记的
		// “一号”逐字相同；样品归属仍直接写中文，证明两种写法可互相匹配。
		raw := `{"points":` + string(ptsJSON) + `,
  "limits": {"x-pH": [{"pointId": "\u4e00\u53f7", "item": "pH", "value": 8, "effective": "` +
			openEff().Format(time.RFC3339Nano) + `"}]},
  "samples": {
    "S1": {
      "id": "S1", "pointId": "一号",
      "sampledAt": "` + at(10, 0).Format(time.RFC3339Nano) + `",
      "measurements": [{"item": "pH", "value": 7}],
      "status": "pending"
    }
  }
}`
		writeRawDiskFile(t, dir, raw)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("中文编号的 Unicode 转义应与已登记编号匹配: %v", err)
		}
		defer s.Close()
		if _, err := s.Confirm("S1"); err != nil {
			t.Fatalf("转义归属中文编号的限值应能参与确认: %v", err)
		}
	})
}

// 编号内部的合法字符仍可使用：注册并引用 "P 1" 是正常数据。
func TestOpenLimitPointIDInternalWhitespaceAccepted(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{"P 1": {ID: "P 1", Name: "内部空白采样点"}},
		Limits: map[string][]Limit{
			diskLimitKey("P 1", openPH): limitOwnershipVersions("P 1", openEff()),
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P 1", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("编号内部空白是合法文本，限值应正常读入: %v", err)
	}
	defer s.Close()
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("归属 P 1 的限值应能参与确认: %v", err)
	}
}

// 编号内部含 U+0000 的合法限值仍可读取：共用旧版单字符拼接键的不同组合各自
// 按记录自身的 pointId 归属已登记采样点，重新分组行为不变。
func TestOpenLimitOwnershipWithNULInsideIDAccepted(t *testing.T) {
	dir := t.TempDir()
	zeroPoint := "P\x00A"
	zeroItem := "A\x00B"
	oldKey := zeroPoint + "\x00" + "B"
	if oldKey != "P"+"\x00"+zeroItem {
		t.Fatal("测试前提：两个组合应拼出同一个旧键")
	}
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			zeroPoint: {ID: zeroPoint, Name: "带零编号"},
			"P":       {ID: "P", Name: "普通编号"},
		},
		Limits: map[string][]Limit{
			oldKey: {
				{PointID: zeroPoint, Item: "B", Value: 10, Effective: openEff()},
				{PointID: "P", Item: zeroItem, Value: 5, Effective: openEff()},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("两组各自归属已登记采样点时应正常读入: %v", err)
	}
	defer s.Close()
	if got := s.limits[limitGroup{pointID: zeroPoint, item: "B"}]; len(got) != 1 || got[0].Value != 10 {
		t.Fatalf("含 U+0000 组合应各自成组（zeroPoint 组）: %+v", got)
	}
	if got := s.limits[limitGroup{pointID: "P", item: zeroItem}]; len(got) != 1 || got[0].Value != 5 {
		t.Fatalf("含 U+0000 组合应各自成组（P 组）: %+v", got)
	}
}

// 共用旧版集合键的两组里，一组归属未登记点时按该组记录自身的采样点与项目
// 报告，不误伤另一组，也不被缺数值、缺生效时间或重复时刻检查抢先报成别的
// 损坏（这版故意同时缺 value 与 effective）。
func TestOpenLimitOwnershipWithinOldCollidedKeyRejected(t *testing.T) {
	dir := t.TempDir()
	zeroPoint := "P\x00A"
	zeroItem := "A\x00B"
	oldKey := zeroPoint + "\x00" + "B"
	if oldKey != "P"+"\x00"+zeroItem {
		t.Fatal("测试前提：两个组合应拼出同一个旧键")
	}
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P": {ID: "P", Name: "普通编号"},
		},
		Limits: map[string][]Limit{
			oldKey: {
				{PointID: "P", Item: zeroItem, Value: 5, Effective: openEff()},
				// zeroPoint 未登记；这版同时缺 value、effective，归属核对必须先报。
				{PointID: zeroPoint, Item: "B"},
			},
		},
	})
	assertOpenLimitOwnershipCorrupt(t, dir,
		[]string{"登记限值", "B", strconv.Quote(zeroPoint), "未登记"},
		[]string{"缺少上限数值", "缺少生效时间"})
}

// 归属不明的限值与其他采样点的完整限值、已确认完整样品同处一个文件：不能只
// 跳过问题限值继续打开，整次失败、无可用存放，其余数据也读不到。
func TestOpenUnregisteredLimitRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	good := &Sample{
		ID: "S-GOOD", PointID: "P2", SampledAt: at(5, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       StatusConfirmed, Exceeded: false,
		Results: []ItemResult{
			{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
		},
	}
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Limits: map[string][]Limit{
			diskLimitKey("P2", openPH): limitOwnershipVersions("P2", openEff()),
			diskLimitKey("P9", openPH): limitOwnershipVersions("P9", openEff()),
		},
		Samples: map[string]*Sample{"S-GOOD": good},
	})
	assertOpenLimitOwnershipCorrupt(t, dir,
		[]string{"登记限值", strconv.Quote("P9"), "未登记"}, nil)
	if _, err := Open(dir); !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("其他限值与样品再正常也不能让整次打开通过，got %v", err)
	}

	// 把 P9 补进已登记采样点后（模拟文件被修复为登记在先），同版限值与正常
	// 样品原样可读，证明失败时没有部分读入或改写。
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
			"P9": {ID: "P9", Name: "九号取水口"},
		},
		Limits: map[string][]Limit{
			diskLimitKey("P2", openPH): limitOwnershipVersions("P2", openEff()),
			diskLimitKey("P9", openPH): limitOwnershipVersions("P9", openEff()),
		},
		Samples: map[string]*Sample{"S-GOOD": good},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("补齐采样点登记后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P2")
	if err != nil || !ok || latest.ID != "S-GOOD" {
		t.Fatalf("正常样品的历史判定依据应原样保留: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 样品和限值同时引用未登记采样点 P9：仍沿用现有的样品归属错误（保留样品编号
// 与未登记说明），而不是报登记限值的归属问题。
func TestOpenSampleAndLimitBothUnregisteredKeepsSampleError(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P9", openPH): limitOwnershipVersions("P9", openEff()),
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P9", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusPending,
			},
		},
	})
	assertOpenLimitOwnershipCorrupt(t, dir,
		[]string{"样品", "S1", strconv.Quote("P9"), "未登记"},
		[]string{"登记限值"})
}

// 失败时不补登记采样点、不改写限值归属：同一份损坏文件重复打开仍失败，且
// 文件字节与首次打开前完全一致。
func TestOpenLimitOwnershipFailureDoesNotMutateFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P9", openPH): limitOwnershipVersions("P9", openEff()),
		},
	})
	before, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	for i := 0; i < 2; i++ {
		if got, err := Open(dir); err == nil {
			got.Close()
			t.Fatalf("第 %d 次打开仍应失败", i+1)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("重复失败的打开不得补登记采样点或改写限值归属")
	}
}

// 没有登记限值的正常数据照常打开；待判定样品是否缺少适用上限仍在确认时判断，
// 历史判定依据不因这项核对重新计算。
func TestOpenNoLimitsUnchangedByOwnershipCheck(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustSample(t, s, "S1", "P1", at(2, 0), Measurement{Item: openPH, Value: 7})
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("缺少适用上限仍应由确认操作判断，got %v", err)
	}
	mustLimit(t, s, "P1", openPH, 8, openEff())
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("补登记限值后确认应成功: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常流程保存的数据应照常打开: %v", err)
	}
	defer s2.Close()
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" || latest.Exceeded {
		t.Fatalf("已有完整样品的状态与历史判定依据应保留: %+v ok=%v err=%v", latest, ok, err)
	}
}
