package water

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// 本文件保护“打开本地数据”时对样品归属的核对：文件中的每份样品都必须明确
// 属于一个已经登记的采样点。只登记了 P1、样品却写着 P9 时，即使其余内容
// （状态、采样时间、原测量、已保存判定依据）全部通过检查，也必须让整次
// Open 失败——否则已确认的错误归属记录甚至能作为 P9 的最近有效结果返回。
// pointId 缺失、为 null、为空字符串或仅含空白都按缺少采样点编号处理；带
// 首尾空白的编号属于损坏，不能去掉空白后替它找到采样点；编号完整但没有
// 对应登记记录时明确说明采样点未登记，不能从限值记录或其他样品推测它存在。
// 匹配按 JSON 解码后的文本逐字进行，不转换大小写，Unicode 转义表示同一
// 文本时视为同一编号。核对适用于待判定、已确认和已作废样品；任一样品不合
// 格则整次失败、返回 nil 存放、错误可识别为 ErrCorruptRecord，原文件字节
// 不变，不补登记采样点、不改写归属或结论。归属完整的记录照常读入，采样点
// 已登记但没有适用限值仍由确认操作判断，空目录与空样品集合照常打开。

// assertOpenPointCorrupt 断言一次因样品归属问题导致的失败 Open：nil 存放、
// ErrCorruptRecord、信息包含全部 want 子串且去掉哨兵前缀后不含任一 notWant
// 子串，原文件字节不变。
func assertOpenPointCorrupt(t *testing.T, dir string, want, notWant []string) {
	t.Helper()
	assertOpenIDCorrupt(t, dir, want, notWant)
}

// pointCheckSample 构造一份除采样点归属外内容完整的样品，便于证明状态、
// 测量与判定依据完整不能代替采样点已经登记。status 决定记录形态：
// confirmed 带完整达标判定，voided 带作废原因。
func pointCheckSample(pointID string, status Status) *Sample {
	smp := idCheckSample("S1", status)
	smp.PointID = pointID
	return smp
}

// sampleJSONWithPointToken 把样品序列化结果中的 "pointId":"P1" 字段整体
// 替换成 pointToken 给出的原始 JSON 片段，用于构造字段缺失、null、空串或
// Unicode 转义等 Go 结构体表达不出的写法。pointToken 为空串表示整个字段
// 缺失（连逗号一起去掉）。调用方须保证 smp.PointID 为 "P1"。
func sampleJSONWithPointToken(t *testing.T, smp *Sample, pointToken string) string {
	t.Helper()
	b, err := json.Marshal(smp)
	if err != nil {
		t.Fatalf("marshal sample: %v", err)
	}
	s := string(b)
	if pointToken == "" {
		old := `"pointId":"` + smp.PointID + `",`
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

// rawStoreWithPointsEntries 用自定义采样点集合拼出完整数据文件文本。
func rawStoreWithPointsEntries(t *testing.T, points map[string]SamplingPoint, entries []string) string {
	t.Helper()
	pts, err := json.Marshal(points)
	if err != nil {
		t.Fatalf("marshal points: %v", err)
	}
	return fmt.Sprintf(`{"points":%s,"samples":{%s}}`, pts, strings.Join(entries, ","))
}

// jsonEscaped 把文本逐字符转成 JSON 的 u-XXXX 转义写法（基本多文种平面内），
// 用于让落盘文本真的出现转义序列而不是直接字符，验证核对按 JSON 解码后的
// 文本进行。
func jsonEscaped(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteString(fmt.Sprintf("\\u%04x", r))
	}
	return b.String()
}

// 任务中的核心场景：文件只登记 P1，样品 S1 却写着 P9，其余内容再完整也必须
// 整次拒绝；信息点名样品 S1 与原采样点编号 P9，并明确说明 P9 未登记。
func TestOpenSamplePointNotRegistered(t *testing.T) {
	cases := []struct {
		name   string
		status Status
	}{
		{"待判定", StatusPending},
		{"已确认且判定依据完整", StatusConfirmed},
		{"已作废", StatusVoided},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDiskFile(t, dir, diskState{
				Points: openPoints(),
				Samples: map[string]*Sample{
					"S1": pointCheckSample("P9", c.status),
				},
			})
			assertOpenPointCorrupt(t, dir,
				[]string{"S1", "P9", "未登记"},
				[]string{"含首尾空白", "缺少采样点编号"})
		})
	}
}

// 已确认样品保存着完整且自洽的超标判定依据，也不能代替采样点已经登记：
// 归属不对时整次打开失败，没人能把它当成 P9 的最近有效结果取走。
func TestOpenConfirmedUnknownPointCannotBecomeLatestResult(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P9", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusConfirmed, Exceeded: true,
				Results: []ItemResult{
					{Item: openPH, Value: 9, Limit: 8, LimitEffective: openEff(), Exceeded: true},
				},
			},
		},
	})
	assertOpenPointCorrupt(t, dir, []string{"S1", "P9", "未登记"}, nil)
}

// pointId 的各种缺失写法都算缺少采样点编号：字段缺失、null、空字符串、
// 仅含空格或制表换行。
func TestOpenSampleMissingPointIDForms(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"字段缺失", ""},
		{"null", `"pointId":null`},
		{"空字符串", `"pointId":""`},
		{"仅含空格", `"pointId":"   "`},
		{"仅含制表与换行", `"pointId":"\t\n "`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			obj := sampleJSONWithPointToken(t, pointCheckSample("P1", StatusPending), c.token)
			writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
				rawEntryWithObject(`"S1"`, obj),
			}))
			assertOpenPointCorrupt(t, dir,
				[]string{"S1", "缺少采样点编号"},
				[]string{"含首尾空白", "未登记"})
		})
	}
}

// 编号带首尾空白属于损坏：不能去掉空白后替它匹配采样点。即使去掉空白恰好
// 是已登记的 P1 也拒绝；带空白的编号本身就未登记时，也要报“含首尾空白”
// 而不是笼统的未登记。错误信息带出原采样点编号。
func TestOpenSamplePointWhitespaceRejected(t *testing.T) {
	cases := []struct {
		name  string
		token string
		raw   string
	}{
		{"两端空格但P1已登记", `"pointId":" P1 "`, " P1 "},
		{"末尾制表但P1已登记", `"pointId":"P1\t"`, `P1\t`}, // 错误信息按 %q 引用，制表符渲染为 \t
		{"前导空格且编号本身未登记", `"pointId":" P9 "`, " P9 "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			obj := sampleJSONWithPointToken(t, pointCheckSample("P1", StatusPending), c.token)
			writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
				rawEntryWithObject(`"S1"`, obj),
			}))
			assertOpenPointCorrupt(t, dir,
				[]string{"S1", "含首尾空白", c.raw},
				[]string{"未登记", "缺少采样点编号"})
		})
	}
}

// 完整编号按 JSON 解码后的文本逐字匹配，不转换大小写：登记的是 P1、样品
// 写 p1 时属于采样点未登记，错误信息带出原编号 p1。
func TestOpenSamplePointComparisonIsCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S1": pointCheckSample("p1", StatusPending),
		},
	})
	assertOpenPointCorrupt(t, dir,
		[]string{"S1", "p1", "未登记"},
		[]string{"含首尾空白"})
}

// Unicode 转义只是 JSON 的另一种写法：pointId 用 P 的转义（U+0050）写出
// P1，解码后与登记的 P1 逐字相同，必须照常读入。落盘文本里真的出现转义
// 序列而不是直接字符。
func TestOpenSamplePointUnicodeEscapeAccepted(t *testing.T) {
	dir := t.TempDir()
	token := `"pointId":"` + jsonEscaped("P1") + `"`
	obj := sampleJSONWithPointToken(t, pointCheckSample("P1", StatusPending), token)
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntryWithObject(`"S1"`, obj),
	}))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Unicode 转义表示同一已登记编号时应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("应读入归属 P1 的样品 S1: %+v err=%v", list, err)
	}
}

// 中文采样点编号与表示同一文本的 Unicode 转义视为同一编号：登记“一号点”，
// 样品用逐字符的转义写出，解码后逐字一致，照常读入。
func TestOpenSamplePointChineseUnicodeEscapeAccepted(t *testing.T) {
	dir := t.TempDir()
	points := map[string]SamplingPoint{
		"一号点": {ID: "一号点", Name: "一号取水口"},
	}
	smp := pointCheckSample("一号点", StatusPending)
	b, err := json.Marshal(smp)
	if err != nil {
		t.Fatalf("marshal sample: %v", err)
	}
	escaped := `"pointId":"` + jsonEscaped("一号点") + `"`
	obj := strings.Replace(string(b), `"pointId":"一号点"`, escaped, 1)
	writeRawDataFile(t, dir, rawStoreWithPointsEntries(t, points, []string{
		rawEntryWithObject(`"S1"`, obj),
	}))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("中文编号的 Unicode 转义写法应正常读入: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("一号点")
	if err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("应读入归属一号点的样品 S1: %+v err=%v", list, err)
	}
}

// 不能从限值记录推测采样点存在：限值里写着 P9（甚至已被这份样品的判定引用），
// 但 points 中没有 P9 的登记记录时，样品仍按采样点未登记拒绝。
func TestOpenPointNotInferredFromLimits(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Limits: map[string][]Limit{
			diskLimitKey("P9", openPH): {
				{PointID: "P9", Item: openPH, Value: 8, Effective: openEff()},
			},
		},
		Samples: map[string]*Sample{
			"S1": pointCheckSample("P9", StatusPending),
		},
	})
	assertOpenPointCorrupt(t, dir, []string{"S1", "P9", "未登记"}, nil)
}

// 不能从其他样品推测采样点存在：S-GOOD 归属 P1 不能为写着 P9 的 S-BAD 作保。
func TestOpenPointNotInferredFromOtherSamples(t *testing.T) {
	dir := t.TempDir()
	bad := pointCheckSample("P9", StatusPending)
	bad.ID = "S-BAD" // 记录自身编号与集合键一致，使归属成为唯一问题
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
			"S-BAD": bad,
		},
	})
	assertOpenPointCorrupt(t, dir, []string{"S-BAD", "P9", "未登记"}, nil)

	// 去掉归属不明的记录后，正常样品原样可读：证明失败时没有部分读入、
	// 也没有补登记 P9 或改写任何归属。
	writeDiskFile(t, dir, diskState{
		Points: openPoints(),
		Samples: map[string]*Sample{
			"S-GOOD": {
				ID: "S-GOOD", PointID: "P1", SampledAt: at(5, 0),
				Measurements: []Measurement{{Item: openPH, Value: 7}},
				Status:       StatusConfirmed, Exceeded: false,
				Results: []ItemResult{
					{Item: openPH, Value: 7, Limit: 8, LimitEffective: openEff(), Exceeded: false},
				},
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修正归属问题后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S-GOOD" {
		t.Fatalf("P1 最近有效结果应指向未被改动的 S-GOOD: %+v ok=%v err=%v", latest, ok, err)
	}
	if _, ok, err := s.LatestResult("P9"); err != nil || ok {
		t.Fatalf("失败后不得补登记 P9: ok=%v err=%v", ok, err)
	}
}

// 归属核对先于状态等逐份检查：pointId 未登记同时 status 缺失时，必须先报
// 采样点未登记，而不是让缺状态抢先报出。
func TestOpenPointProblemReportedBeforeOtherDamage(t *testing.T) {
	dir := t.TempDir()
	obj := sampleJSONWithPointToken(t, pointCheckSample("P9", StatusPending), `"pointId":"P9"`)
	obj = strings.Replace(obj, `,"status":"pending"`, "", 1)
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntryWithObject(`"S1"`, obj),
	}))
	assertOpenPointCorrupt(t, dir,
		[]string{"S1", "P9", "未登记"},
		[]string{"缺少状态"})
}

// 一个文件里其他样品再正常，也不能只读入正常部分：归属问题让整次打开失败，
// 且原文件字节保持不变（不补登记采样点、不改写任何归属）。坏样品的记录 id
// 与集合键一致，使归属成为唯一问题。
func TestOpenPointProblemKeepsFileByteIdentical(t *testing.T) {
	dir := t.TempDir()
	bad := pointCheckSample("P9", StatusPending)
	bad.ID = "S-BAD"
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S-GOOD"), idCheckSample("S-GOOD", StatusConfirmed)),
		rawEntry(t, strconv.Quote("S-BAD"), bad),
	}))
	assertOpenPointCorrupt(t, dir, []string{"S-BAD", "P9", "未登记"}, nil)
}

// 采样点已登记但尚未登记限值的待判定样品仍可打开：归属核对只管采样点是否
// 已登记，是否缺少采样当时适用的上限继续由确认操作判断。
func TestOpenRegisteredPointWithoutLimitPendingOpens(t *testing.T) {
	dir := t.TempDir()
	writeDiskFile(t, dir, diskState{
		Points: map[string]SamplingPoint{
			"P1": {ID: "P1", Name: "一号取水口"},
			"P2": {ID: "P2", Name: "二号取水口"},
		},
		Samples: map[string]*Sample{
			"S1": {
				ID: "S1", PointID: "P2", SampledAt: at(10, 0),
				Measurements: []Measurement{{Item: openPH, Value: 9}},
				Status:       StatusPending,
			},
		},
	})
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("采样点已登记但无限值的待判定样品应正常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P2")
	if err != nil || len(list) != 1 || list[0].ID != "S1" || list[0].Status != StatusPending {
		t.Fatalf("应原样读入待判定样品: %+v err=%v", list, err)
	}
	if _, err := s.Confirm("S1"); !errors.Is(err, ErrMissingLimit) {
		t.Fatalf("缺少适用上限仍应由确认操作拒绝（ErrMissingLimit），got %v", err)
	}
}

// 没有样品的空目录与空样品集合照常打开（归属核对只针对文件中的样品）。
func TestOpenPointCheckAllowsEmptyStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("全新空目录应正常打开: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	writeDiskFile(t, dir, diskState{
		Points:  openPoints(),
		Samples: map[string]*Sample{},
	})
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("空样品集合应正常打开: %v", err)
	}
	s2.Close()
}

// 正常录入保存的数据归属始终完整：登记采样点、登记限值、录入、确认、作废后
// 重新打开，状态、测量、历史判定依据、按点列表与最近有效结果的排序和作废
// 资格都保持原有行为。
func TestOpenPointOwnershipNormalFlowUnchanged(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", openPH, 8, openEff())
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	conf := mustConfirm(t, s, "S1")
	if !conf.Exceeded || conf.PointID != "P1" {
		t.Fatalf("确认结果应保留归属 P1 与超标结论: %+v", conf)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("正常流程保存的文件应正常打开: %v", err)
	}
	defer s2.Close()
	latest, ok, err := s2.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" || latest.PointID != "P1" {
		t.Fatalf("最近有效结果应保留归属 P1 的 S1: %+v ok=%v err=%v", latest, ok, err)
	}

	// 作废后重开：作废记录仍按归属 P1 可查，最近有效结果跳过它。
	if _, err := s2.Void("S1", "录入信息有误"); err != nil {
		t.Fatalf("Void: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatalf("作废后重新打开应正常: %v", err)
	}
	defer s3.Close()
	if list, err := s3.ListByPoint("P1"); err != nil || len(list) != 1 ||
		list[0].Status != StatusVoided || list[0].PointID != "P1" {
		t.Fatalf("作废记录仍应归属 P1 并可按点查看: %+v err=%v", list, err)
	}
	if _, ok, err := s3.LatestResult("P1"); err != nil || ok {
		t.Fatalf("已作废样品应被最近有效结果跳过: ok=%v err=%v", ok, err)
	}
}
