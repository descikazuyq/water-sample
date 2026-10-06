package water

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// 本文件保护“打开本地数据”时样品编号唯一性核对的覆盖面：
// 唯一性针对整份文件中所有会被当作样品集合读取的条目，而不仅是单个 samples
// 对象内部。文件顶层写出多个 samples 对象时，JSON 反序列化会把它们合并进同一
// 个 map，同一编号分散在不同对象里同样只剩幸存条目；字段名的大小写写法
// （Samples 等）也会被反序列化识别为样品集合。这些条目都纳入同一次编号核对：
// 同一编号无论出现在同一个集合还是不同集合、中间是否隔着其他样品或顶层字段、
// 内容是否完全相同，都在打开时整体拒绝；后面再出现空集合或 null 也不能让前面
// 集合里已经出现的重复编号逃过检查。多个非空集合没有重复编号时沿用现有读取
// 行为，不因集合字段出现多次而拒绝。

// rawStoreWithSamplesObjects 拼出顶层含多个样品集合对象的原始 JSON：
// 每个 objs 元素是一个完整的 `"samples":{...}`（或大小写写法、null、空对象）
// 顶层字段文本，按给定顺序放在 points 之后。
func rawStoreWithSamplesObjects(t *testing.T, objs ...string) string {
	t.Helper()
	pts, err := json.Marshal(openPoints())
	if err != nil {
		t.Fatalf("marshal points: %v", err)
	}
	fields := append([]string{`"points":` + string(pts)}, objs...)
	return "{" + strings.Join(fields, ",") + "}"
}

func samplesField(entries ...string) string {
	return `"samples":{` + strings.Join(entries, `,`) + `}`
}

// 任务中的核心场景：S1 先在一份 samples 集合里保存测量值 9、上限 8、已确认
// 超标，另一份顶层 samples 集合又保存同编号的测量值 7、上限 8、已确认达标。
// 打开时不能让后者替换原结论：整次 Open 失败、nil 存放、ErrCorruptRecord，
// 错误信息指出重复编号并说明是样品集合中的编号重复，原文件字节不变。
func TestOpenDuplicateSampleIDAcrossCollections(t *testing.T) {
	dir := t.TempDir()
	raw := rawStoreWithSamplesObjects(t,
		samplesField(rawEntry(t, strconv.Quote("S1"), dupExceededS1())),
		samplesField(rawEntry(t, strconv.Quote("S1"), dupCompliantS1())),
	)
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")

	// 不能留下任何内部状态：修正成只保留超标那一条后，同目录即可正常打开，
	// 且读回的是超标结论（证明此前没有悄悄读入达标版本）。
	writeRawDataFile(t, dir, rawStoreWithEntries(t, []string{
		rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
	}))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("去除重复编号后应正常打开: %v", err)
	}
	defer s.Close()
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok || latest.ID != "S1" {
		t.Fatalf("最近有效结果应指向 S1: %+v ok=%v err=%v", latest, ok, err)
	}
	if !latest.Exceeded || latest.Results[0].Value != 9 || latest.Results[0].Limit != 8 {
		t.Fatalf("已确认的超标依据必须原样保留: %+v", latest)
	}
}

// 两次出现之间隔着其他样品和其他顶层字段，一样算重复。
func TestOpenDuplicateSampleIDAcrossCollectionsNonAdjacent(t *testing.T) {
	dir := t.TempDir()
	other := &Sample{
		ID: "S-OTHER", PointID: "P1", SampledAt: at(11, 0),
		Measurements: []Measurement{{Item: openPH, Value: 5}},
		Status:       StatusPending,
	}
	limits, err := json.Marshal(map[string][]Limit{})
	if err != nil {
		t.Fatalf("marshal limits: %v", err)
	}
	raw := rawStoreWithSamplesObjects(t,
		samplesField(rawEntry(t, strconv.Quote("S1"), dupExceededS1())),
		`"limits":`+string(limits),
		samplesField(
			rawEntry(t, strconv.Quote("S-OTHER"), other),
			rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
		),
	)
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 分散在不同集合里的两条记录内容完全相同，也按重复编号拒绝，不合并接受。
func TestOpenDuplicateSampleIDAcrossCollectionsIdentical(t *testing.T) {
	dir := t.TempDir()
	raw := rawStoreWithSamplesObjects(t,
		samplesField(rawEntry(t, strconv.Quote("S1"), dupExceededS1())),
		samplesField(rawEntry(t, strconv.Quote("S1"), dupExceededS1())),
	)
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 编号按 JSON 解码后的完整文本判断：一个集合里直接写 "S1"，另一个集合里把
// S 写成 Unicode 转义（"S1" 解码后仍是 S1），必须识别为重复。
func TestOpenDuplicateSampleIDAcrossCollectionsUnicodeEscape(t *testing.T) {
	dir := t.TempDir()
	raw := rawStoreWithSamplesObjects(t,
		samplesField(rawEntry(t, `"S1"`, dupExceededS1())),
		// 文件中的原始键标记是 Unicode 转义写出的 S（\u0053）加字面量 1；
		// Go 原始字符串字面量里反斜杠按原样落盘。
		samplesField(rawEntry(t, `"\u00531"`, dupCompliantS1())),
	)
	writeRawDataFile(t, dir, raw)
	assertOpenRejectsDuplicateSampleID(t, dir, "S1")
}

// 字段名的大小写写法（Samples）同样会被反序列化读取为样品集合，也必须纳入
// 同一次编号核对：不能借换大小写绕过。
func TestOpenDuplicateSampleIDCaseVariantFieldName(t *testing.T) {
	t.Run("大小写集合内部重复", func(t *testing.T) {
		dir := t.TempDir()
		raw := rawStoreWithSamplesObjects(t,
			`"Samples":{`+strings.Join([]string{
				rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
				rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
			}, ",")+`}`,
		)
		writeRawDataFile(t, dir, raw)
		assertOpenRejectsDuplicateSampleID(t, dir, "S1")
	})
	t.Run("跨大小写集合重复", func(t *testing.T) {
		dir := t.TempDir()
		raw := rawStoreWithSamplesObjects(t,
			samplesField(rawEntry(t, strconv.Quote("S1"), dupExceededS1())),
			`"Samples":{`+rawEntry(t, strconv.Quote("S1"), dupCompliantS1())+`}`,
		)
		writeRawDataFile(t, dir, raw)
		assertOpenRejectsDuplicateSampleID(t, dir, "S1")
	})
	t.Run("大小写集合被正常读取", func(t *testing.T) {
		// 先证明 "Samples" 写法确实会被当作样品集合读入（否则谈不上绕过）：
		// 内容合法时应正常打开并查得到样品。
		dir := t.TempDir()
		raw := rawStoreWithSamplesObjects(t,
			`"Samples":{`+rawEntry(t, strconv.Quote("S1"), dupExceededS1())+`}`,
		)
		writeRawDataFile(t, dir, raw)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Samples 大小写写法应被识别为样品集合并正常读入: %v", err)
		}
		defer s.Close()
		if list, err := s.ListByPoint("P1"); err != nil || len(list) != 1 || list[0].ID != "S1" {
			t.Fatalf("Samples 集合中的样品应可读: %+v err=%v", list, err)
		}
	})
}

// 后面再出现空集合或 null，也不能让前面集合里已经出现的重复编号逃过检查。
func TestOpenDuplicateSampleIDNotEscapedByTrailingEmptyOrNull(t *testing.T) {
	cases := []struct {
		name    string
		trailer string
	}{
		{"尾部空集合", `"samples":{}`},
		{"尾部null", `"samples":null`},
		{"尾部大小写空集合", `"Samples":{}`},
		{"尾部大小写null", `"Samples":null`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := rawStoreWithSamplesObjects(t,
				samplesField(
					rawEntry(t, strconv.Quote("S1"), dupExceededS1()),
					rawEntry(t, strconv.Quote("S1"), dupCompliantS1()),
				),
				c.trailer,
			)
			writeRawDataFile(t, dir, raw)
			assertOpenRejectsDuplicateSampleID(t, dir, "S1")
		})
	}
	// 跨集合重复同样不能被尾部的空集合或 null 遮盖。
	for _, c := range cases {
		t.Run("跨集合"+c.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := rawStoreWithSamplesObjects(t,
				samplesField(rawEntry(t, strconv.Quote("S1"), dupExceededS1())),
				samplesField(rawEntry(t, strconv.Quote("S1"), dupCompliantS1())),
				c.trailer,
			)
			writeRawDataFile(t, dir, raw)
			assertOpenRejectsDuplicateSampleID(t, dir, "S1")
		})
	}
}

// 多个非空样品集合没有重复编号、其余内容合法时，沿用现有读取行为：不能仅因
// 集合字段出现多次就一概拒绝，所有集合里的样品都照常读入。
func TestOpenMultipleCollectionsWithoutDuplicateID(t *testing.T) {
	dir := t.TempDir()
	a := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusPending,
	}
	b := &Sample{
		ID: "S2", PointID: "P1", SampledAt: at(11, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       StatusPending,
	}
	raw := rawStoreWithSamplesObjects(t,
		samplesField(rawEntry(t, strconv.Quote("S1"), a)),
		samplesField(rawEntry(t, strconv.Quote("S2"), b)),
	)
	writeRawDataFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("多个无重复编号的样品集合应正常打开: %v", err)
	}
	defer s.Close()
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 2 {
		t.Fatalf("两个集合里的样品都应读入: %+v err=%v", list, err)
	}
	if list[0].ID != "S2" || list[1].ID != "S1" {
		t.Fatalf("按点查询顺序不变（采样时间从晚到早）: %+v", list)
	}
}

// 空集合与缺失的样品集合照常打开：多个空集合、空集合与 null 混排都不构成重复。
func TestOpenMultipleEmptyCollectionsNotDuplicateID(t *testing.T) {
	dir := t.TempDir()
	writeRawDataFile(t, dir, rawStoreWithSamplesObjects(t,
		`"samples":{}`, `"Samples":{}`, `"samples":null`,
	))
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("空集合与 null 混排应正常打开: %v", err)
	}
	defer s.Close()
}

// 分散在不同集合里的不同样品使用同一个测量项目，不算编号重复。
func TestOpenSameItemAcrossCollectionsIsNotDuplicateID(t *testing.T) {
	dir := t.TempDir()
	a := &Sample{
		ID: "S1", PointID: "P1", SampledAt: at(10, 0),
		Measurements: []Measurement{{Item: openPH, Value: 9}},
		Status:       StatusPending,
	}
	b := &Sample{
		ID: "S2", PointID: "P1", SampledAt: at(11, 0),
		Measurements: []Measurement{{Item: openPH, Value: 7}},
		Status:       StatusPending,
	}
	raw := rawStoreWithSamplesObjects(t,
		samplesField(rawEntry(t, strconv.Quote("S1"), a)),
		samplesField(rawEntry(t, strconv.Quote("S2"), b)),
	)
	writeRawDataFile(t, dir, raw)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("不同集合里的样品各含 pH 是正常数据: %v", err)
	}
	defer s.Close()
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 2 {
		t.Fatalf("两份不同编号的样品都应读入: %+v err=%v", list, err)
	}
}

// 跨集合重复编号的错误信息与单份样品内部测量项目重复保持可区分（集合层措辞
// 由 assertOpenRejectsDuplicateSampleID 核对，这里对照内部重复场景）。
func TestOpenCrossCollectionMessageDistinctFromDuplicateItem(t *testing.T) {
	dir := t.TempDir()
	writeRawDataFile(t, dir, rawStoreWithSamplesObjects(t,
		samplesField(rawEntry(t, strconv.Quote("S1"), dupExceededS1())),
		samplesField(rawEntry(t, strconv.Quote("S1"), dupCompliantS1())),
	))
	_, err := Open(dir)
	if err == nil {
		t.Fatalf("跨集合编号重复必须让整次 Open 失败")
	}
	msg := err.Error()
	if !strings.Contains(msg, "样品集合中存在重复的样品编号") {
		t.Fatalf("跨集合编号重复应说明样品集合中存在重复编号，got %v", err)
	}
	// 与“同一份样品内部测量项目重复”的区分体现在说明文字上：集合层错误
	// 明确指出这是样品集合内的编号重复，而不是测量项目重复。
	if !strings.Contains(msg, "不同于同一份样品内部的测量项目重复") {
		t.Fatalf("错误信息应能与样品内部测量项目重复区分，got %v", err)
	}
}

// 正常录入行为不受多集合核对影响：同编号同内容返回原记录、内容不同拒绝。
func TestOpenResubmitUnchangedAfterCrossCollectionFix(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	first := mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	again, err := s.SubmitSample("S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 9})
	if err != nil || again.ID != first.ID {
		t.Fatalf("同编号同内容应返回原样品: %+v err=%v", again, err)
	}
	if _, err := s.SubmitSample("S1", "P1", at(10, 0), Measurement{Item: openPH, Value: 7}); !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("同编号内容不同仍应整体拒绝 ErrSampleConflict，got %v", err)
	}
	defer s.Close()
}
