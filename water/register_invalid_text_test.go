package water

import (
	"errors"
	"testing"
)

// 本文件的测试只针对一件事：RegisterPoint 只接受能作为合法 UTF-8 原样保存的
// 编号和名称。任一字段含孤立 0xFF、0xFE 字节或不完整的多字节序列时，整次登记
// 必须返回编号、名称均为空的采样点和可被 errors.Is(err, water.ErrInvalidText)
// 识别的文本编码错误：不占用编号、不改动已有采样点的名称及其关联限值与样品，
// 也不能被归为空字段或重复编号。即使另一项为空白，或合法编号已经登记而这次
// 名称含非法字节，也必须报告编码问题。真正的 U+FFFD（“�”）、中文和文本内部的
// U+0000 仍然接受；文本处理只去首尾空白。

func registerExpectEncodingError(t *testing.T, s *Store, label, id, name string) {
	t.Helper()
	got, err := s.RegisterPoint(id, name)
	assertEncodingError(t, err, label)
	if errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("%s: 编码问题不能被归为重复编号: %v", label, err)
	}
	if got.ID != "" || got.Name != "" {
		t.Fatalf("%s: 失败必须返回编号、名称均为空的采样点，实际为 %+v", label, got)
	}
}

// 编号或名称含非法 UTF-8 字节时整次拒绝：末尾分别带孤立 0xFF、0xFE 的两个
// 编号是不同的提交，但都不能登记成功；不完整的多字节序列同理。即使另一项
// 为空白，也必须报告编码错误而不是空字段错误。
func TestRegisterPointRejectsInvalidUTF8(t *testing.T) {
	s, dir := open(t)
	// 先成功登记一个采样点，使落盘文件存在，便于断言失败登记没有任何字节落盘
	mustPoint(t, s, "P0", "对照点")

	registerExpectEncodingError(t, s, "编号末尾 0xFF", "P\xff", "取水口")
	registerExpectEncodingError(t, s, "编号末尾 0xFE", "P\xfe", "取水口")
	registerExpectEncodingError(t, s, "编号含截断的三字节序列", "P\xe4\xb8", "取水口")
	registerExpectEncodingError(t, s, "编号开头不完整序列", "\xe4\xb8P", "取水口")
	registerExpectEncodingError(t, s, "非法序列夹在编号中间", "P"+"\xc0\xaf"+"1", "取水口")
	registerExpectEncodingError(t, s, "名称含孤立 0xFF", "P1", "取水口"+"\xff")
	registerExpectEncodingError(t, s, "名称含孤立 0xFE", "P2", "取水"+"\xfe"+"口")
	registerExpectEncodingError(t, s, "名称为截断的多字节字符", "P3", "取水口"+"\xe4\xb8")

	// 即使另一项（去空白后）为空，编码问题也优先报告
	registerExpectEncodingError(t, s, "非法编号+空白名称", "P4"+"\xff", "   ")
	registerExpectEncodingError(t, s, "空白编号+非法名称", "   ", "取水口"+"\xfe")
	registerExpectEncodingError(t, s, "编号和名称都非法", "P5"+"\xff", "x"+"\xfe")

	// 全部拒绝之后：除对照点外没有任何采样点留下，落盘文件中没有替换字符
	if len(s.points) != 1 {
		t.Fatalf("被拒绝的登记不得留下采样点，points=%v", s.points)
	}
	if got := s.points["P0"]; got.Name != "对照点" {
		t.Fatalf("对照点不应被改动: %+v", got)
	}
	assertNoReplacementBytes(t, dir, "全部拒绝后")

	// 曾被失败请求使用的编号改成合法名称后应能正常首次登记
	p, err := s.RegisterPoint("P1", "一号取水口")
	if err != nil {
		t.Fatalf("失败登记不得占用编号: %v", err)
	}
	if p.ID != "P1" || p.Name != "一号取水口" {
		t.Fatalf("合法登记返回内容异常: %+v", p)
	}
}

// checkPHExceededSaved 校验前置样品（pH 9 > 上限 8）的已确认判定依据原样保存。
func checkPHExceededSaved(t *testing.T, smp Sample, label string) {
	t.Helper()
	if smp.ID != "S1" || smp.PointID != "P1" || smp.Status != StatusConfirmed {
		t.Fatalf("%s: 样品基本字段被改动: %+v", label, smp)
	}
	if !smp.SampledAt.Equal(at(10, 0)) {
		t.Fatalf("%s: 采样时间被改动: %+v", label, smp)
	}
	if !smp.Exceeded || len(smp.Results) != 1 {
		t.Fatalf("%s: 整份结论或判定结果数量被改动: %+v", label, smp)
	}
	r := smp.Results[0]
	if r.Item != "pH" || r.Value != 9 || r.Limit != 8.0 || !r.LimitEffective.Equal(at(1, 0)) || !r.Exceeded {
		t.Fatalf("%s: pH 项判定依据被改动: %+v", label, r)
	}
}

// 用已有编号提交非法名称（或非法编号恰好撞上已登记编号的字节形态）必须直接
// 失败：原采样点名称不变，其限值、样品和已保存的判定依据仍归属于原来的点；
// 关闭后重新打开同一数据目录，也不能出现由失败登记产生或改名的采样点。
func TestRegisterPointInvalidUTF8DoesNotTouchExistingPoint(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	checkPHExceededSaved(t, mustConfirm(t, s, "S1"), "前置确认")

	// 已有编号 + 非法名称：报告编码错误，不能改名，也不能走重复编号分支
	registerExpectEncodingError(t, s, "已有编号+非法名称", "P1", "二号取水口"+"\xff")
	registerExpectEncodingError(t, s, "已有编号+不完整序列名称", " P1 ", "二号取水口"+"\xe4\xb8")

	// 原采样点名称与关联记录保持不变
	if got := s.points["P1"]; got.ID != "P1" || got.Name != "一号取水口" {
		t.Fatalf("已有采样点被失败登记改动: %+v", got)
	}
	versions := s.limits[limitGroup{pointID: "P1", item: "pH"}]
	if len(versions) != 1 || versions[0].Value != 8.0 {
		t.Fatalf("已有限值被失败登记改动: %+v", versions)
	}
	checkPHExceededSaved(t, mustConfirm(t, s, "S1"), "非法登记后重复确认")
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 {
		t.Fatalf("按点查看记录变化: %+v err=%v", list, err)
	}
	checkPHExceededSaved(t, list[0], "非法登记后按点查看")

	// 重新打开同一数据目录：原来的点和判定依据原样保存，没有失败登记的痕迹
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertNoReplacementBytes(t, dir, "重开前")
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got := s2.points["P1"]; got.ID != "P1" || got.Name != "一号取水口" {
		t.Fatalf("重开后采样点变化: %+v", got)
	}
	reopened, err := s2.ListByPoint("P1")
	if err != nil || len(reopened) != 1 {
		t.Fatalf("重开后记录变化: %+v err=%v", reopened, err)
	}
	checkPHExceededSaved(t, reopened[0], "重开后")
	// 失败登记中那些处理后为空或新造的编号都不应出现
	for _, id := range []string{"P2", "P3", "P4", "P5"} {
		if _, ok := s2.points[id]; ok {
			t.Fatalf("重开后不应出现失败登记产生的采样点 %q", id)
		}
	}
}

// 真正的“�”（U+FFFD）、中文、文本内部的 U+0000 与内部空白都是合法字符，
// 继续接受；只去首尾空白，不替换、不删除字符。登记成功返回的编号和名称与
// 保存内容一致，之后仍可用处理后的编号登记限值、录入样品并按点查看。
func TestRegisterPointAcceptsValidUnicodeText(t *testing.T) {
	s, dir := open(t)

	cases := []struct {
		id, name string
	}{
		{"P-�-1", "取水�口"},     // 真正的替换字符 U+FFFD
		{"P-中文", "一号取水口"},     // 中文
		{"P\x001", "内 部 空 白"}, // 内部 U+0000 与内部空白
		{"P-emoji", "溶氧🫧"},    // 有效四字节字符
	}
	for _, c := range cases {
		p, err := s.RegisterPoint("  "+c.id+"  ", "  "+c.name+"  ")
		if err != nil {
			t.Fatalf("合法 Unicode 文本 %q 不应被拒绝: %v", c.id, err)
		}
		if p.ID != c.id || p.Name != c.name {
			t.Fatalf("只应去掉首尾空白，实际为 %+v", p)
		}
	}
	if len(s.points) != len(cases) {
		t.Fatalf("不同编号应各自登记，points=%v", s.points)
	}

	// 用处理后的编号继续登记限值、录入样品并确认、按点查看
	mustLimit(t, s, "P-�-1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "  P-�-1  ", at(10, 0), Measurement{Item: "pH", Value: 7})
	smp := mustConfirm(t, s, "S1")
	if smp.PointID != "P-�-1" || smp.Results[0].Limit != 8.0 || smp.Exceeded {
		t.Fatalf("判定依据应归属于合法登记的采样点: %+v", smp)
	}
	list, err := s.ListByPoint("P-�-1")
	if err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("按点查看异常: %+v err=%v", list, err)
	}

	// 重开后编号与名称原样保存
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	for _, c := range cases {
		if got := s2.points[c.id]; got.ID != c.id || got.Name != c.name {
			t.Fatalf("采样点 %q 在保存数据中缺失或被改写: %+v", c.id, got)
		}
	}
}

// 合法文本沿用既有规则：处理后为空报 ErrEmptyField；重复编号报
// ErrDuplicatePoint，不因名称不同而更新原记录。
func TestRegisterPointValidationOrderAndDuplicates(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "一号取水口")

	// 合法文本的空字段仍是 ErrEmptyField，而不是编码错误
	if _, err := s.RegisterPoint("   ", "取水口"); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("空白编号应报 ErrEmptyField，got %v", err)
	}
	if _, err := s.RegisterPoint("P2", "   "); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("空白名称应报 ErrEmptyField，got %v", err)
	}

	// 重复编号仍是 ErrDuplicatePoint，名称不同也不更新原记录
	p, err := s.RegisterPoint(" P1 ", "完全不同的名称")
	if !errors.Is(err, ErrDuplicatePoint) {
		t.Fatalf("重复编号应报 ErrDuplicatePoint，got %v", err)
	}
	if p.ID != "" || p.Name != "" {
		t.Fatalf("被拒绝的登记必须返回空采样点，实际为 %+v", p)
	}
	if got := s.points["P1"]; got.Name != "一号取水口" {
		t.Fatalf("重复编号登记不得改名: %+v", got)
	}
}

// 已关闭的数据存放仍按原有规则返回 ErrClosed，编码检查不改变这一点。
func TestRegisterPointInvalidUTF8OnClosedStore(t *testing.T) {
	s, _ := open(t)
	mustPoint(t, s, "P1", "一号取水口")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.RegisterPoint("P2"+"\xff", "取水口"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭应返回 ErrClosed，got %v", err)
	}
	if _, err := s.RegisterPoint("P1", "取水口"+"\xff"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭应返回 ErrClosed，got %v", err)
	}
	if _, err := s.RegisterPoint("P2", "二号取水口"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭应返回 ErrClosed，got %v", err)
	}
}
