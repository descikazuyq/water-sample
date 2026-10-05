package water

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件的测试只针对一件事：录入只接受能作为合法 UTF-8 原样保存的文本。
// 样品编号、采样点编号或任一项目名含孤立 0xFF、0xFE 字节或不完整的多字节
// 序列时，整份提交必须返回空样品和可被 errors.Is(err, water.ErrInvalidText)
// 识别的专用文本编码错误：不留下部分记录（哪怕非法项目排在最后）、不占用
// 样品编号、不改变按点查看与最近有效结果，也不能被归为未知采样点、项目重复、
// 同编号冲突或走重复录入成功分支。真正的 U+FFFD（“�”）、中文、其它有效
// 多字节字符以及内部 U+0000 仍然接受；文本处理只去首尾空白。

// notEncodingError 之外的这些错误都不允许冒充编码问题。
func assertEncodingError(t *testing.T, err error, label string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidText) {
		t.Fatalf("%s: 应返回 ErrInvalidText，实际为 %v", label, err)
	}
	for _, target := range []error{
		ErrEmptyField, ErrUnknownPoint, ErrInvalidValue, ErrInvalidTime,
		ErrNoMeasurements, ErrDuplicateItem, ErrSampleConflict,
	} {
		if errors.Is(err, target) {
			t.Fatalf("%s: 编码问题不能被归为 %v: %v", label, target, err)
		}
	}
}

func assertNoReplacementBytes(t *testing.T, dir, label string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "water-data.json"))
	if err != nil {
		t.Fatalf("%s: 读取落盘文件: %v", label, err)
	}
	if bytes.Contains(data, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("%s: 被拒绝的非法文本被替换成 U+FFFD 落盘", label)
	}
}

// 非法文本出现在编号、采样点编号或任一项目名（含排在最后的项目）时整份拒绝，
// 不留下部分记录；即使提交同时还带着未知采样点、项目重复、非有限数值、
// 零采样时间等其它问题，也必须报告编码问题。
func TestSubmitSampleRejectsInvalidUTF8(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))

	bad := []string{
		"S\xff",           // 孤立 0xFF
		"S\xfe",           // 孤立 0xFE
		"S\xe4\xb8",       // 不完整的三字节序列
		"S\xc0\xaf",       // 非法多字节序列
		"pH" + "\xff",     // 项目名末尾的孤立 0xFF
		"浊度" + "\xfe",     // 合法中文后接孤立 0xFE
		"\xe4\xb8" + "pH", // 项目名开头不完整序列
	}

	submit := func(label, id, point string, at time.Time, ms ...Measurement) {
		t.Helper()
		got, err := s.SubmitSample(id, point, at, ms...)
		assertEncodingError(t, err, label)
		if got.ID != "" || got.PointID != "" || got.Status != "" || len(got.Measurements) != 0 {
			t.Fatalf("%s: 失败必须返回空样品，实际为 %+v", label, got)
		}
	}

	// 编号非法
	submit("编号含 0xFF", bad[0], "P1", at(10, 0), Measurement{Item: "pH", Value: 7})
	submit("编号含 0xFE", bad[1], "P1", at(10, 0), Measurement{Item: "pH", Value: 7})
	submit("编号含不完整序列", bad[2], "P1", at(10, 0), Measurement{Item: "pH", Value: 7})
	// 采样点编号非法，即使该编号看起来未登记也不能报未知采样点
	submit("采样点含 0xFF", "S-new-1", "P1"+"\xff", at(10, 0), Measurement{Item: "pH", Value: 7})
	submit("编号与采样点都非法且采样点未登记", "S9"+"\xfe", "P9", at(10, 0), Measurement{Item: "pH", Value: 7})
	// 项目名非法：排在最前、夹在中间、排在最后都要整份拒绝，不留部分记录
	submit("首项非法", "S-new-2", "P1", at(10, 0),
		Measurement{Item: bad[6], Value: 1}, Measurement{Item: "COD", Value: 20})
	submit("中间项非法", "S-new-3", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: bad[3], Value: 1}, Measurement{Item: "COD", Value: 20})
	submit("末项非法且其余合法", "S-new-4", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 20}, Measurement{Item: bad[4], Value: 1})
	submit("末项为不完整序列", "S-new-5", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: bad[5], Value: 1})

	// 编码问题优先于其它一切内容校验
	submit("非法项目+项目重复", "S-new-6", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 1}, Measurement{Item: "pH", Value: 2}, Measurement{Item: bad[4], Value: 1})
	submit("非法编号+零采样时间", bad[0], "P1", time.Time{}, Measurement{Item: "pH", Value: 7})
	submit("非法项目+非有限数值", "S-new-7", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: bad[4], Value: math.Inf(1)})

	// 全部拒绝之后：没有任何样品落盘或留在内存，编号没有被占用
	if len(s.samples) != 0 {
		t.Fatalf("被拒绝的提交不得留下记录，samples=%v", s.samples)
	}
	for _, id := range []string{"S-new-1", "S-new-2", "S-new-3", "S-new-4", "S-new-5", "S-new-6", "S-new-7"} {
		if _, err := s.Confirm(id); !errors.Is(err, ErrUnknownSample) {
			t.Fatalf("被拒绝编号 %s 不应能确认，got %v", id, err)
		}
	}
	if list, err := s.ListByPoint("P1"); err != nil || len(list) != 0 {
		t.Fatalf("按点查看不应出现被拒绝的记录: %+v err=%v", list, err)
	}
	if latest, ok, err := s.LatestResult("P1"); err != nil || ok || latest.ID != "" {
		t.Fatalf("最近有效结果不应变化: %+v ok=%v err=%v", latest, ok, err)
	}
	assertNoReplacementBytes(t, dir, "全部拒绝后")

	// 先前失败占用过的编号改成合法文本后应能正常首次录入
	saved := mustSample(t, s, "S-new-4", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 7}, Measurement{Item: "COD", Value: 25})
	if saved.ID != "S-new-4" || saved.Status != StatusPending {
		t.Fatalf("合法文本应能首次录入: %+v", saved)
	}
}

// 用已有已确认样品的编号提交非法文本：必须报编码错误，原测量值、状态、
// 逐项所用限值与生效时间、整份结论保持原样；不会走幂等成功或冲突分支。
func TestSubmitInvalidUTF8DoesNotTouchExistingSample(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustLimit(t, s, "P1", "COD", 30.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30})
	if _, err := s.Confirm("S1"); err != nil {
		t.Fatalf("Confirm S1: %v", err)
	}

	// 与已存内容完全一致、只是多带一个非法项目：不能幂等成功
	got, err := s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 30},
		Measurement{Item: "x\xff", Value: 1})
	assertEncodingError(t, err, "已存编号+非法项目")
	if got.ID != "" || got.Status != "" {
		t.Fatalf("失败必须返回空样品，实际为 %+v", got)
	}
	// 非法采样点编号、非法项目名（即使内容本会冲突）也一样
	_, err = s.SubmitSample("S1", "P1"+"\xfe", at(10, 0), Measurement{Item: "pH", Value: 9})
	assertEncodingError(t, err, "已存编号+非法采样点")
	_, err = s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD", Value: 99})
	// 对照：全合法但内容不同仍是既有冲突错误
	if !errors.Is(err, ErrSampleConflict) {
		t.Fatalf("对照应报同编号冲突，got %v", err)
	}
	_, err = s.SubmitSample("S1", "P1", at(10, 0),
		Measurement{Item: "pH", Value: 9}, Measurement{Item: "COD\xff", Value: 99})
	assertEncodingError(t, err, "已存编号+非法项目且值不同")

	// 原样品完整保持已确认状态与全部判定依据
	checkTwoItemSaved(t, mustConfirm(t, s, "S1"), "非法提交后重复确认")
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("按点查看数量变化: %+v err=%v", list, err)
	}
	checkTwoItemSaved(t, list[0], "非法提交后按点查看")
	latest, ok, err := s.LatestResult("P1")
	if err != nil || !ok {
		t.Fatalf("最近有效结果变化: %+v ok=%v err=%v", latest, ok, err)
	}
	checkTwoItemSaved(t, latest, "非法提交后最近有效结果")

	// 重新打开后原样仍在，落盘文件中没有替换字符
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertNoReplacementBytes(t, dir, "重开前")
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	reopened, err := s2.ListByPoint("P1")
	if err != nil || len(reopened) != 1 {
		t.Fatalf("重开后记录变化: %+v err=%v", reopened, err)
	}
	checkTwoItemSaved(t, reopened[0], "重开后")
}

func mustConfirm(t *testing.T, s *Store, id string) Sample {
	t.Helper()
	smp, err := s.Confirm(id)
	if err != nil {
		t.Fatalf("Confirm(%q): %v", id, err)
	}
	return smp
}

// “�”（U+FFFD）本身是合法字符，中文、其它有效多字节字符和内部 U+0000
// 都照常接受；只去首尾空白，不做替换、大小写转换或额外归一化。成功录入后
// 编号、采样点归属和项目名在返回记录、按点查看与重开后的保存数据中保持一致。
func TestSubmitSampleAcceptsValidUnicodeText(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")

	// 含 U+FFFD、中文、emoji（四字节序列）、内部 U+0000 以及与 "pH" 仅大小写
	// 不同的 "PH" 的项目名各自登记限值：若做大小写归一化，"pH"/"PH" 会撞成同一项。
	items := []string{
		"浊�度",     // 真正的替换字符 U+FFFD
		"浊度",      // 中文
		"溶氧🫧",     // 有效四字节字符
		"ph\x00x", // 内部 U+0000 继续接受
		"pH",
		"PH",
	}
	for _, item := range items {
		mustLimit(t, s, "P1", item, 100.0, at(1, 0))
	}

	// 编号含 U+FFFD、首尾空白；项目名首尾空白只做去除
	id := "  S-�-01  "
	ms := []Measurement{
		{Item: "  浊�度  ", Value: 1},
		{Item: "浊度", Value: 2},
		{Item: "溶氧🫧", Value: 3},
		{Item: "ph\x00x", Value: 4},
		{Item: "  pH  ", Value: 5},
		{Item: "PH", Value: 6},
	}
	smp, err := s.SubmitSample(id, "  P1  ", at(10, 0), ms...)
	if err != nil {
		t.Fatalf("合法 Unicode 文本不应被拒绝: %v", err)
	}
	if smp.ID != "S-�-01" || smp.PointID != "P1" || len(smp.Measurements) != len(items) {
		t.Fatalf("只应去掉首尾空白，实际为 %+v", smp)
	}
	gotItems := map[string]float64{}
	for _, m := range smp.Measurements {
		gotItems[m.Item] = m.Value
	}
	for _, m := range ms {
		if gotItems[strings.TrimSpace(m.Item)] != m.Value {
			t.Fatalf("项目名在返回记录中不一致: %+v", smp.Measurements)
		}
	}

	// 去首尾空白后相同仍是重复项目
	if _, err := s.SubmitSample("S-dup", "P1", at(10, 0),
		Measurement{Item: " pH ", Value: 1}, Measurement{Item: "pH", Value: 2}); !errors.Is(err, ErrDuplicateItem) {
		t.Fatalf("去空白后相同的项目名仍属重复，got %v", err)
	}

	// 含特殊字符的项目同样能按其名取到限值并完成确认
	confirmed := mustConfirm(t, s, smp.ID)
	if confirmed.Status != StatusConfirmed || len(confirmed.Results) != len(items) {
		t.Fatalf("确认结果异常: %+v", confirmed)
	}
	rs := map[string]ItemResult{}
	for _, r := range confirmed.Results {
		if r.Limit != 100.0 || !r.LimitEffective.Equal(at(1, 0)) {
			t.Fatalf("逐项限值异常: %+v", r)
		}
		rs[r.Item] = r
	}
	for _, item := range items {
		if _, ok := rs[item]; !ok {
			t.Fatalf("项目 %q 在确认结果中缺失: %+v", item, confirmed.Results)
		}
	}

	// 按点查看与返回记录一致
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("按点查看异常: %+v err=%v", list, err)
	}
	listed := list[0]
	if listed.ID != smp.ID {
		t.Fatalf("按点查看记录不一致: %+v", listed)
	}
	if listed.PointID != "P1" || len(listed.Measurements) != len(items) {
		t.Fatalf("按点查看记录不一致: %+v", listed)
	}
	listItems := map[string]bool{}
	for _, m := range listed.Measurements {
		listItems[m.Item] = true
	}
	for _, item := range items {
		if !listItems[item] {
			t.Fatalf("项目 %q 在按点查看中缺失", item)
		}
	}

	// 重开后保存的数据与录入时一致
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	reopened, err := s2.ListByPoint("P1")
	if err != nil || len(reopened) != 1 {
		t.Fatalf("重开后按点查看异常: %+v err=%v", reopened, err)
	}
	fromDisk := reopened[0]
	if fromDisk.ID != "S-�-01" {
		t.Fatalf("重开后记录与录入不一致: %+v", fromDisk)
	}
	if fromDisk.PointID != "P1" || fromDisk.Status != StatusConfirmed || len(fromDisk.Measurements) != len(items) {
		t.Fatalf("重开后记录与录入不一致: %+v", fromDisk)
	}
	diskItems := map[string]bool{}
	for _, m := range fromDisk.Measurements {
		diskItems[m.Item] = true
	}
	for _, item := range items {
		if !diskItems[item] {
			t.Fatalf("项目 %q 在保存数据中缺失或被改写", item)
		}
	}
	// 全部测量值均不超过上限 100，重开后整份结论应原样保存为达标
	if fromDisk.Exceeded || len(fromDisk.Results) != len(items) {
		t.Fatalf("整份结论或逐项依据在保存数据中不一致: %+v", fromDisk)
	}
}
