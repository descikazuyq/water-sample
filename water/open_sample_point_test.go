package water

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件保护“打开本地数据”时对样品采样点归属的核对：每份样品的 pointId
// 必须完整（字段缺失、为 null、为空字符串或仅含空白都算缺少采样点编号），
// 不含首尾空白，且按 JSON 解码后的文本与本文件已登记的采样点编号逐字匹配
// （不转大小写；Unicode 转义表示同一文本可以匹配）。编号完整但没有对应登记
// 记录时，必须明确报该采样点未登记，不能凭限值记录或其他样品推测它存在。
// 核对适用于待判定、已确认和已作废样品；只要一份样品不符合要求，整次 Open
// 失败、返回 nil 存放、错误可识别为 ErrCorruptRecord，信息点到样品编号并
// 区分三种问题，含首尾空白与未登记时带出原采样点编号；其他样品正常也不能
// 只读入正常部分，原文件字节不变。

// pointCheckSample 构造一份除采样点归属外内容完整的样品，便于证明测量与
// 判定依据完整不能代替采样点已经登记。confirmed 带完整达标判定，voided
// 带作废原因（待判定后直接作废、无历史依据）。
func pointCheckSample(id, pointID string, status Status) *Sample {
	smp := &Sample{
		ID:           id,
		PointID:      pointID,
		SampledAt:    at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       status,
	}
	switch status {
	case StatusConfirmed:
		smp.Exceeded = false
		smp.Results = []ItemResult{
			{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
		}
	case StatusVoided:
		smp.VoidReason = "录入信息有误"
	}
	return smp
}

// sampleJSONWithPointToken 把样品序列化结果中的 "pointId":"..." 字段整体
// 替换成 pointToken 给出的原始 JSON 片段；空串表示整个字段缺失。
func sampleJSONWithPointToken(t *testing.T, smp *Sample, pointToken string) string {
	t.Helper()
	b, err := json.Marshal(smp)
	if err != nil {
		t.Fatalf("marshal sample: %v", err)
	}
	s := string(b)
	if pointToken == "" {
		old := `,"pointId":"` + smp.PointID + `"`
		if !strings.Contains(s, old) {
			t.Fatalf("序列化结果中找不到采样点字段 %q：%s", old, s)
		}
		return strings.Replace(s, old, "", 1)
	}
	old := `"pointId":"` + smp.PointID + `"`
	if !strings.Contains(s, old) {
		t.Fatalf("序列化结果中找不到采样点字段 %q：%s", old, s)
	}
	return strings.Replace(s, old, pointToken, 1)
}

// assertOpenPointCorrupt 打开并断言：整次失败、nil 存放、ErrCorruptRecord，
// 去掉哨兵前缀后的具体原因包含所有 want 子串、且不包含任一 notWant 子串，
// 原文件字节不变。
func assertOpenPointCorrupt(t *testing.T, dir string, want, notWant []string) {
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
		t.Fatalf("归属有问题的样品必须让整次 Open 失败")
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

// 任务中的核心场景：文件只登记了 P1，已确认样品 S1 的 pointId 却写着 P9，
// 其余内容（测量、判定依据、超标标记）完整也必须整次拒绝；否则它甚至能作为
// P9 的最近有效结果返回。
func TestOpenConfirmedSampleBelongsToUnregisteredPoint(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": pointCheckSample("S1", "P9", StatusConfirmed),
		},
	})
	assertOpenPointCorrupt(t, dir,
		[]string{"S1", "P9", "未登记"},
		[]string{"缺少采样点编号", "含首尾空白"})

	// 打开失败返回 nil 存放：P9 不可能通过任何查询拿到这份“最近有效结果”。
	s, err := Open(dir)
	if err == nil {
		s.Close()
		t.Fatalf("归属不明的记录仍在文件中时打开必须持续失败")
	}
}

// 归属核对适用于待判定、已确认和已作废三种状态；作废记录仍供按点核对，
// 已确认样品的判定依据完整也不能代替采样点已经登记。
func TestOpenUnknownPointAppliesToAllStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status Status
	}{
		{"待判定", StatusPending},
		{"已确认", StatusConfirmed},
		{"已作废", StatusVoided},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Samples: map[string]*Sample{
					"S1": pointCheckSample("S1", "P9", c.status),
				},
			})
			assertOpenPointCorrupt(t, dir, []string{"S1", "P9", "未登记"}, nil)
		})
	}
}

// pointId 字段的各种缺失写法都算缺少采样点编号：字段缺失、null、空字符串、
// 仅含空白；后两种通过 Go 结构体也能构造，前两种必须用原始 JSON。
func TestOpenMissingPointIDForms(t *testing.T) {
	cases := []struct {
		name    string
		raw     bool // true 时用原始 JSON 片段构造
		token   string
		struct_ string // raw=false 时结构体直接使用的 pointId
	}{
		{"字段缺失", true, "", ""},
		{"null", true, `"pointId":null`, ""},
		{"空字符串", false, "", ""},
		{"仅含空格", false, "", "   "},
		{"仅含制表与换行", false, "", "\t\n "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.raw {
				obj := sampleJSONWithPointToken(t, pointCheckSample("S1", "P1", StatusPending), c.token)
				writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
					rawEntryWithObject(`"S1"`, obj),
				}))
			} else {
				writeDiskFile(t, dir, diskState{
					Points: openPoints(),
					Samples: map[string]*Sample{
						"S1": pointCheckSample("S1", c.struct_, StatusPending),
					},
				})
			}
			assertOpenPointCorrupt(t, dir,
				[]string{"S1", "缺少采样点编号"},
				[]string{"未登记", "含首尾空白"})
		})
	}
}

// 带首尾空白的编号属于损坏：即使去掉空白后正好是已登记的 P1，也不能替它
// 找到采样点；错误信息带出原采样点编号。
func TestOpenPointIDWithSurroundingWhitespaceRejected(t *testing.T) {
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
				Samples: map[string]*Sample{
					"S1": pointCheckSample("S1", c.pointID, StatusPending),
				},
			})
			assertOpenPointCorrupt(t, dir,
				[]string{"S1", "含首尾空白", strconv.Quote(c.pointID)},
				[]string{"未登记", "缺少采样点编号"})
		})
	}
}

// 编号按 JSON 解码后的文本逐字匹配、不转换大小写：P1 已登记，样品写 p1
// 时按采样点未登记拒绝，错误带出原编号 p1。
func TestOpenPointIDComparisonIsCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": pointCheckSample("S1", "p1", StatusConfirmed),
		},
	})
	assertOpenPointCorrupt(t, dir,
		[]string{"S1", strconv.Quote("p1"), "未登记"},
		[]string{"含首尾空白", "缺少采样点编号"})
}

// Unicode 转义只是 JSON 的另一种写法：pointId 用 P1 表示 P1，解码后
// 与已登记编号是同一文本，必须正常读入；中文编号与其 Unicode 转义同理。
func TestOpenPointIDUnicodeEscapeSameTextAccepted(t *testing.T) {
	t.Run("P1转义", func(t *testing.T) {
		dir := t.TempDir()
		// 文件里真的出现 \u00501 这样的六个转义字符（P = U+0050），JSON
		// 解码后是 P1，与已登记编号是同一文本。
		obj := sampleJSONWithPointToken(t, pointCheckSample("S1", "P1", StatusPending),
			"\"pointId\":\"\\u00501\"")
		writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
			rawEntryWithObject(`"S1"`, obj),
		}))
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Unicode 转义表示同一已登记编号时应正常读入: %v", err)
		}
		defer s.Close()
		list, err := s.ListByPoint("P1")
		if err != nil || len(list) != 1 || list[0].PointID != "P1" {
			t.Fatalf("样品应归属已登记的 P1: %+v err=%v", list, err)
		}
	})

	t.Run("中文编号转义", func(t *testing.T) {
		dir := t.TempDir()
		pts := map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"一号": {ID: "一号", Name: "中文采样点"},
		}
		// 一 = U+4E00，号 = U+53F7：转义写法解码后与已登记的“一号”逐字相同。
		obj := sampleJSONWithPointToken(t, pointCheckSample("S1", "一号", StatusPending),
			"\"pointId\":\"\\u4e00\\u53f7\"")
		ptsJSON, err := json.Marshal(pts)
		if err != nil {
			t.Fatalf("marshal points: %v", err)
		}
		writeRawDataFile(t, dir, `{"points":`+string(ptsJSON)+`,"samples":{`+
			`"S1":`+obj+`}}`)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("中文编号的 Unicode 转义应与已登记编号匹配: %v", err)
		}
		defer s.Close()
		if list, err := s.ListByPoint("一号"); err != nil || len(list) != 1 {
			t.Fatalf("样品应归属中文编号采样点: %+v err=%v", list, err)
		}
	})
}

// 编号内部的合法字符仍可使用：注册并引用 "P 1" 是正常数据。
func TestOpenPointIDInternalWhitespaceAccepted(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{"P 1": {ID: "P 1", Name: "内部空白采样点"}},
		Samples: map[string]*Sample{
			"S1": pointCheckSample("S1", "P 1", StatusPending),
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("编号内部空白是合法文本，应正常读入: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P 1"); err != nil || len(list) != 1 {
		t.Fatalf("样品应归属 P 1: %+v err=%v", list, err)
	}
}

// 不能凭限值记录推测采样点存在：文件中存在归属 P9 的限值，但 points 没有
// P9 时，pointId 为 P9 的样品仍按采样点未登记拒绝。
func TestOpenUnregisteredPointNotInferredFromLimits(t *testing.T) {
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
	assertOpenPointCorrupt(t, dir, []string{"S1", "P9", "未登记"}, nil)
}

// 不能凭其他样品推测采样点存在：两份样品都写 P9 不能让 P9 变成已登记。
func TestOpenUnregisteredPointNotInferredFromOtherSamples(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": pointCheckSample("S1", "P9", StatusPending),
			"S2": pointCheckSample("S2", "P9", StatusConfirmed),
		},
	})
	assertOpenPointCorrupt(t, dir, []string{"未登记", "P9"}, nil)
}

// 归属核对先于状态等其他逐份检查：pointId 缺失时即使 status 也缺失，也必须
// 先报缺少采样点编号；pointId 含空白时即使状态非法，也必须先报含首尾空白。
func TestOpenPointCheckReportedBeforeOtherDamage(t *testing.T) {
	t.Run("缺少编号先于缺状态", func(t *testing.T) {
		dir := t.TempDir()
		obj := sampleJSONWithPointToken(t, pointCheckSample("S1", "P1", StatusPending), "")
		obj = strings.Replace(obj, `,"status":"pending"`, "", 1)
		writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
			rawEntryWithObject(`"S1"`, obj),
		}))
		assertOpenPointCorrupt(t, dir,
			[]string{"S1", "缺少采样点编号"},
			[]string{"缺少状态"})
	})

	t.Run("含空白先于非法状态", func(t *testing.T) {
		dir := t.TempDir()
		smp := pointCheckSample("S1", " P1 ", StatusPending)
		b, err := json.Marshal(smp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		obj := strings.Replace(string(b), `"status":"pending"`, `"status":"bogus"`, 1)
		writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
			rawEntryWithObject(`"S1"`, obj),
		}))
		assertOpenPointCorrupt(t, dir,
			[]string{"S1", "含首尾空白"},
			[]string{"bogus"})
	})
}

// 同文件其他样品正常也不能只读入正常部分：一份归属不明的样品让整次打开失败；
// 修复文件后正常样品的状态与结论原样可读。失败的打开不回写原文件。
func TestOpenPointProblemRejectsWholeFile(t *testing.T) {
	dir := t.TempDir()
	good := &Sample{
		ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       StatusConfirmed, Exceeded: false,
		Results: []ItemResult{
			{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
		},
	}
	bad := pointCheckSample("S-BAD", "P9", StatusConfirmed)
	writeDiskFile(t, dir, diskState{
		Points:  openPoints(),
		Samples: map[string]*Sample{"S-GOOD": good, "S-BAD": bad},
	})
	assertOpenPointCorrupt(t, dir, []string{"S-BAD", "P9", "未登记"}, nil)

	// 移除归属不明的记录后，正常样品原样可读：证明失败时没有部分读入或改写。
	writeDiskFile(t, dir, diskState{
		Points:  openPoints(),
		Samples: map[string]*Sample{"S-GOOD": good},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("移除归属不明记录后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S-GOOD" {
		t.Fatalf("最近有效结果应指向未被改动的正常样品: %+v ok=%v err=%v", latest, ok, err)
	}
}

// 失败时不补登记采样点、不改写样品归属：同一份损坏文件重复打开仍失败，
// 且文件字节与首次打开前完全一致。
func TestOpenPointFailureDoesNotMutateFile(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": pointCheckSample("S1", "P9", StatusPending),
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
		t.Fatalf("重复失败的打开不得补登记采样点或改写归属")
	}
}

// 采样点已登记但尚未登记限值的待判定样品仍可打开；是否缺少采样当时适用的
// 上限继续由确认操作判断（ErrMissingLimit）。
func TestOpenPendingSampleWithoutLimitsStillOpens(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": pointCheckSample("S1", "P1", StatusPending),
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("采样点已登记、限值未登记的待判定样品应正常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].Status != StatusPending {
		t.Fatalf("待判定样品应原样读入: %+v err=%v", list, err)
	}
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("缺少适用上限仍应由确认操作判断，got %v", err)
	}
}

// 正常流程保存的数据（含录入、确认、作废、重开）归属始终完整，读入后状态、
// 测量、历史判定依据、按点列表与最近有效结果的排序和作废资格保持不变。
func TestOpenNormalFlowUnchangedAfterPointCheck(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustPoint(t, s, "P2", "二号取水口")
	mustLimit(t, s, "P1", openPH, 8, openEff())
	mustLimit(t, s, "P2", openPH, 8, openEff())
	mustSample(t, s, "S1", "P1", at(2, 0), Measurement{Item: openPH, Value: 7}) // 早、待判定后作废
	mustSample(t, s, "S2", "P2", at(3, 0), Measurement{Item: openPH, Value: 9}) // 已确认超标
	if _, err := s.Confirm("S2"); err != nil {
		t.Fatalf("Confirm S2: %v", err)
	}
	if _, err := s.Void("S1", "录入信息有误"); err != nil {
		t.Fatalf("Void S1: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常保存的文件应照常打开: %v", err)
	}
	defer s2.Close()

	p1, err := s2.ListByPoint("P1")
	if err != nil || len(p1) != 1 || p1[0].ID != "S1" || p1[0].Status != StatusVoided {
		t.Fatalf("P1 的作废记录应原样保留: %+v err=%v", p1, err)
	}
	if latest, ok, err := s2.LatestResult("P1"); err != nil || ok {
		t.Fatalf("P1 应无最近有效结果: %+v ok=%v err=%v", latest, ok, err)
	}
	latest, ok, err := s2.LatestResult("P2")
	if err != nil || !ok || latest.ID != "S2" || !latest.Exceeded {
		t.Fatalf("P2 最近有效结果应保留 S2 的超标结论: %+v ok=%v err=%v", latest, ok, err)
	}
	if r := latest.Results[0]; r.Limit != 8 || !r.LimitEffective.Equal(openEff()) || !r.Exceeded {
		t.Fatalf("历史判定依据必须原样保留: %+v", r)
	}
	if _, err := s2.Confirm("S1"); !errors.Is(err, ErrVoided) {
		t.Fatalf("作废资格应保持不变，got %v", err)
	}
}

// 没有样品的空目录、样品集合为空或缺失的已有数据都照常打开，不要求存在样品
// 归属。
func TestOpenEmptyDataStillOpensAfterPointCheck(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("全新空目录应照常打开: %v", err)
	}
	s.Close()

	writeDiskFile(t, dir, diskState{Points: openPoints(), Samples: map[string]*Sample{}})
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("空样品集合应照常打开: %v", err)
	}
	s2.Close()

	writeRawDataFile(t, dir, `{"points": {"P1": {"id": "P1", "name": "一号取水口"}}}`)
	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("样品集合缺失应照常打开: %v", err)
	}
	s3.Close()
}
