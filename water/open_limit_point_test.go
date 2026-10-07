package water

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 本文件保护“打开本地数据”时对登记限值采样点归属的核对：每一版限值记录自身
// 保存的 pointId 必须完整（字段缺失、为 null、为空字符串或仅含空白都算缺少
// 采样点编号）、不含首尾空白，且按 JSON 解码后的文本与本文件已登记的采样点
// 编号逐字匹配（不转大小写；Unicode 转义表示同一文本可以匹配）。核对不看保存
// 这组限值的集合键。编号完整但没有对应登记记录时，必须明确报该采样点未登记，
// 不能凭限值或样品反过来补登记。这条核对针对文件中实际存在的每一版，与当前
// 有没有样品、这一版是否已被样品采用无关；任一版不符合要求，整次 Open 失败、
// 返回 nil 存放、错误可识别为 ErrCorruptRecord，信息指出这是登记限值的归属
// 问题、点到测量项目并区分三种问题，含首尾空白与未登记时带出原编号；不能只
// 读入其他正常记录，原文件字节不变。样品和限值同时引用未登记采样点时，仍
// 沿用现有的样品归属错误。

// assertOpenRejectsLimitPoint 断言打开因某版登记限值的采样点归属问题而整次
// 失败：返回 nil 存放、错误可识别为 ErrCorruptRecord，去掉哨兵前缀后的具体
// 原因包含所有 want 子串、且不包含任一 notWant 子串，原文件字节不变。
func assertOpenRejectsLimitPoint(t *testing.T, dir string, want, notWant []string) {
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
		t.Fatalf("归属有问题的登记限值必须让整次 Open 失败")
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

// 任务中的核心场景：文件只登记了 P1，限值集合键也写着 P1，记录自身的 pointId
// 却是未登记的 P9——数值与生效时间完整也不能读入这版归属不明的上限；否则后来
// 登记 P9 并确认样品时，会用上这条原本无法通过正常登记入口写入的限值。核对
// 对象是记录自身的 pointId，不是保存这组限值的集合键。
func TestOpenLimitBelongsToUnregisteredPoint(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			// 集合键声称是 P1/pH，记录自身却归属 P9：以记录自身为准。
			diskLimitKey("P1", openPH): {
				{PointID: "P9", Item: openPH, Value: 8, Effective: openEff()},
			},
		},
	})
	assertOpenRejectsLimitPoint(t, dir,
		[]string{"登记限值", openPH, "P9", "未登记"},
		[]string{"缺少采样点编号", "含首尾空白"})

	// 打开失败返回 nil 存放：这版上限不可能被读入，更不可能在登记 P9 后被
	// 确认样品采用。
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatalf("归属不明的限值仍在文件中时打开必须持续失败")
	}
}

// 归属核对与有没有样品无关：文件尚未录入任何样品时，归属不明的限值同样让
// 整次打开失败，不能等确认某份样品时才报告。
func TestOpenLimitPointCheckWithoutSamples(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P9", openPH): {
				{PointID: "P9", Item: openPH, Value: 8, Effective: openEff()},
			},
		},
	})
	assertOpenRejectsLimitPoint(t, dir,
		[]string{"登记限值", openPH, "P9", "未登记"}, nil)
}

// 归属核对针对文件中实际存在的每一版：旧版或尚未生效的未来版本归属不明同样
// 拒绝，不能只检查当前会被采用的版本；同组另有归属完整的版本也不能遮住问题。
func TestOpenLimitOldAndFutureVersionsOwnershipChecked(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P1", openPH): {
				{PointID: "P1", Item: openPH, Value: 8, Effective: openEff()},
			},
			diskLimitKey("P9", openPH): {
				{PointID: "P9", Item: openPH, Value: 7, Effective: at(2, 0)},  // 旧版
				{PointID: "P9", Item: openPH, Value: 6, Effective: at(20, 0)}, // 未来版
			},
		},
	})
	assertOpenRejectsLimitPoint(t, dir,
		[]string{"登记限值", openPH, "P9", "未登记"}, nil)
}

// pointId 字段的各种缺失写法都算缺少采样点编号：字段缺失、null、空字符串、
// 仅含空白；后两种通过 Go 结构体也能构造，前两种必须用原始 JSON。
func TestOpenLimitMissingPointIDForms(t *testing.T) {
	eff := openEff().Format(time.RFC3339Nano)
	cases := []struct {
		name string
		raw  string // 非空时直接写原始 JSON，否则用结构体构造且 pointId 取 raw 以外的值
		id   string // raw 为空时结构体使用的 pointId
	}{
		{"字段缺失", `{"item": "pH", "value": 8, "effective": "` + eff + `"}`, ""},
		{"null", `{"pointId": null, "item": "pH", "value": 8, "effective": "` + eff + `"}`, ""},
		{"空字符串", "", ""},
		{"仅含空格", "", "   "},
		{"仅含制表与换行", "", "\t\n "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.raw != "" {
				writeRawDiskFile(t, dir, `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [`+c.raw+`]}
}`)
			} else {
				writeDiskFile(t, dir, diskState{
					Points: openPoints(),
					Limits: map[string][]Limit{
						diskLimitKey("P1", openPH): {
							{PointID: c.id, Item: openPH, Value: 8, Effective: openEff()},
						},
					},
				})
			}
			assertOpenRejectsLimitPoint(t, dir,
				[]string{"登记限值", openPH, "缺少采样点编号"},
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
					diskLimitKey("P1", openPH): {
						{PointID: c.pointID, Item: openPH, Value: 8, Effective: openEff()},
					},
				},
			})
			assertOpenRejectsLimitPoint(t, dir,
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
			diskLimitKey("p1", openPH): {
				{PointID: "p1", Item: openPH, Value: 8, Effective: openEff()},
			},
		},
	})
	assertOpenRejectsLimitPoint(t, dir,
		[]string{"登记限值", openPH, strconv.Quote("p1"), "未登记"},
		[]string{"含首尾空白", "缺少采样点编号"})
}

// Unicode 转义只是 JSON 的另一种写法：pointId 用 P1 表示 P1，解码后与
// 已登记编号是同一文本，必须正常读入；中文编号与其 Unicode 转义同理。
func TestOpenLimitPointIDUnicodeEscapeSameTextAccepted(t *testing.T) {
	eff := openEff().Format(time.RFC3339Nano)
	t.Run("P1转义", func(t *testing.T) {
		dir := t.TempDir()
		// 文件里真的出现 P1 这样的转义写法（P = U+0050），JSON 解码后
		// 是 P1，与已登记编号是同一文本。
		writeRawDiskFile(t, dir, `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [{"pointId": "P1", "item": "pH", "value": 8, "effective": "`+eff+`"}]}
}`)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Unicode 转义表示同一已登记编号时应正常读入: %v", err)
		}
		defer s.Close()
		if got := s.limits[limitGroup{pointID: "P1", item: openPH}]; len(got) != 1 || got[0].Value != 8 {
			t.Fatalf("限值应归属已登记的 P1: %+v", got)
		}
	})

	t.Run("中文编号转义", func(t *testing.T) {
		dir := t.TempDir()
		// 一 = U+4E00，号 = U+53F7：转义写法解码后与已登记的“一号”逐字相同。
		writeRawDiskFile(t, dir, `{
  "points": {"一号": {"id": "一号", "name": "中文采样点"}},
  "limits": {"任意键": [{"pointId": "一号", "item": "pH", "value": 8, "effective": "`+eff+`"}]}
}`)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("中文编号的 Unicode 转义应与已登记编号匹配: %v", err)
		}
		defer s.Close()
		if got := s.limits[limitGroup{pointID: "一号", item: openPH}]; len(got) != 1 {
			t.Fatalf("限值应归属中文编号采样点: %+v", got)
		}
	})
}

// 同文件其他限值与样品再正常也不能只读入正常部分：一版归属不明的限值让整次
// 打开失败；修复文件后正常数据原样可读。失败的打开不回写原文件。
func TestOpenLimitPointProblemRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	good := diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Limits: map[string][]Limit{
			diskLimitKey("P2", openPH): {
				{PointID: "P2", Item: openPH, Value: 8, Effective: openEff()},
			},
			diskLimitKey("P9", openPH): {
				{PointID: "P9", Item: openPH, Value: 8, Effective: openEff()},
			},
		},
		Samples: map[string]*Sample{
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P2", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	}
	writeDiskFile(t, dir, good)
	assertOpenRejectsLimitPoint(t, dir, []string{"登记限值", "P9", "未登记"}, nil)

	// 补登记 P9 后（正常登记入口要求采样点先登记），同一版限值合法读入，
	// 正常样品的状态与结论原样保留。
	fixed := good
	fixed.Points["P9"] = SamplingPoint{ID: "P9", Name: "九号取水口"}
	writeDiskFile(t, dir, fixed)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("补登记采样点后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P2")
	if err != nil || !ok || latest.ID != "S-GOOD" {
		t.Fatalf("最近有效结果应指向未被改动的正常样品: %+v ok=%v err=%v", latest, ok, err)
	}
	// 补登记后这版上限可以正常参与判定：P9 的新样品确认时采用它。
	mustSample(t, s, "S9", "P9", at(10, 0), Measurement{Item: openPH, Value: 9})
	conf, err := s.Confirm("S9")
	if err != nil || !conf.Exceeded || conf.Results[0].Limit != 8 {
		t.Fatalf("补登记后限值应正常参与判定: %+v err=%v", conf, err)
	}
}

// 样品和限值同时引用未登记采样点时，仍沿用现有的样品归属错误：
// 错误信息保留样品编号与未登记说明，不报成限值归属问题。
func TestOpenSampleAndLimitBothUnknownPointReportsSample(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P9", openPH): {
				{PointID: "P9", Item: openPH, Value: 8, Effective: openEff()},
			},
		},
		Samples: map[string]*Sample{
			"S1": pointCheckSample("S1", "P9", StatusPending),
		},
	})
	assertOpenRejectsLimitPoint(t, dir,
		[]string{"S1", "P9", "未登记"},
		[]string{"登记限值"})
}

// 归属完整的限值继续按各自的采样点和项目分组读取：共用旧版集合键的不同组合
// 以及编号内部含 U+0000 的合法记录不受这条核对影响。
func TestOpenLimitPointCheckKeepsLegitimateGroups(t *testing.T) {
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
		t.Fatalf("归属完整的 U+0000 组合应正常读入: %v", err)
	}
	defer s.Close()
	if got := s.limits[limitGroup{pointID: zeroPoint, item: "B"}]; len(got) != 1 || got[0].Value != 10 {
		t.Fatalf("zeroPoint 组应原样读入: %+v", got)
	}
	if got := s.limits[limitGroup{pointID: "P", item: zeroItem}]; len(got) != 1 || got[0].Value != 5 {
		t.Fatalf("P 组应原样: %+v", got)
	}
}

// 失败时不补登记采样点、不改写限值归属：同一份损坏文件重复打开仍失败，
// 且文件字节与首次打开前完全一致。
func TestOpenLimitPointFailureDoesNotMutateFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P9", openPH): {
				{PointID: "P9", Item: openPH, Value: 8, Effective: openEff()},
			},
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

// 正常流程保存的数据归属始终完整：登记、确认、作废、重开后限值与样品原样
// 可读，已有完整样品的状态与历史判定依据不因这项核对重新计算。
func TestOpenNormalFlowUnchangedAfterLimitPointCheck(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8, openEff())
	mustSample(t, s, "S1", "P1", at(2, 0), Measurement{Item: openPH, Value: 9})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常保存的文件应照常打开: %v", err)
	}
	defer s2.Close()
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" || !latest.Exceeded {
		t.Fatalf("已确认样品的结论应原样保留: %+v ok=%v err=%v", latest, ok, err)
	}
	if r := latest.Results[0]; r.Limit != 8 || !r.LimitEffective.Equal(openEff()) || !r.Exceeded {
		t.Fatalf("历史判定依据必须原样保留: %+v", r)
	}
}

// 没有限值的正常数据照常打开：空数据、只有采样点或只有样品的数据都不受这条
// 核对影响；样品是否缺少适用上限仍在确认时判断。
func TestOpenNoLimitsStillOpensAfterLimitPointCheck(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": pointCheckSample("S1", "P1", StatusPending),
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("没有限值的数据应正常打开: %v", err)
	}
	defer s.Close()
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("缺少适用上限仍应由确认操作判断，got %v", err)
	}
}

// 归属不明的限值与缺少数值、缺少生效时间等其他限值问题同时存在时，整次打开
// 同样失败；这里只证明归属核对不会放过任何一种组合，具体报哪一条不限定。
func TestOpenLimitPointProblemCombinedWithOtherLimitDamage(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {
    "P1-pH": [{"pointId": "P1", "item": "pH", "value": 8, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}],
    "P9-pH": [{"pointId": "P9", "item": "pH", "value": 8, "effective": "` +
		openEff().Format(time.RFC3339Nano) + `"}]
  }
}`
	writeRawDiskFile(t, dir, raw)
	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("归属不明的限值必须让整次 Open 失败")
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
	if !strings.Contains(err.Error(), "P9") {
		t.Fatalf("错误信息应带出归属不明的采样点编号 P9，实际 %q", err.Error())
	}
}

// 限值记录为 JSON null 时不能让空记录蒙混进可用数据：整次打开失败，
// 错误可识别为 ErrCorruptRecord（空记录同时缺少生效时间，报哪一条不限定）。
func TestOpenNullLimitRecordRejected(t *testing.T) {
	dir := t.TempDir()
	writeRawDiskFile(t, dir, `{
  "points": {"P1": {"id": "P1", "name": "一号取水口"}},
  "limits": {"P1-pH": [null]}
}`)
	got, err := Open(dir)
	if err == nil {
		if got != nil {
			got.Close()
		}
		t.Fatalf("空限值记录必须让整次 Open 失败")
	}
	if got != nil {
		t.Fatalf("打开失败时不得返回可用的数据存放对象，got %#v", got)
	}
	if !errors.Is(err, ErrCorruptRecord) {
		t.Fatalf("应返回 ErrCorruptRecord，got %v", err)
	}
}

// 编号内部的合法字符仍可使用：注册 "P 1" 并为它登记限值是正常数据。
func TestOpenLimitPointIDInternalWhitespaceAccepted(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{"P 1": {ID: "P 1", Name: "内部空白采样点"}},
		Limits: map[string][]Limit{
			diskLimitKey("P 1", openPH): {
				{PointID: "P 1", Item: openPH, Value: 8, Effective: openEff()},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("编号内部空白是合法文本，应正常读入: %v", err)
	}
	defer s.Close()
	if got := s.limits[limitGroup{pointID: "P 1", item: openPH}]; len(got) != 1 {
		t.Fatalf("限值应归属 P 1: %+v", got)
	}
}
