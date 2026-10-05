package water

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 本文件的测试只针对一件事：RegisterPoint 只接受能作为合法 UTF-8 原样保存的
// 编号和名称。任一字段含孤立 0xFF、0xFE 字节或不完整的多字节序列时，整次
// 登记必须返回空采样点和可被 errors.Is(err, water.ErrInvalidText) 识别的文本
// 编码错误：不占用编号、不改变已有采样点的名称及其关联的限值与样品，落盘文件
// 中也不能出现替换字符。即使另一项为空白，或编号已登记而这次名称含非法字节，
// 也必须报告编码问题。真正的 U+FFFD（“�”）、中文和文本内部的 U+0000、内部
// 空白仍然接受；文本处理只去首尾空白。

func rejectPoint(t *testing.T, s *Store, label, id, name string) {
	t.Helper()
	got, err := s.RegisterPoint(id, name)
	assertEncodingError(t, err, label)
	for _, target := range []error{ErrEmptyField, ErrDuplicatePoint, ErrUnknownPoint} {
		if errors.Is(err, target) {
			t.Fatalf("%s: 编码问题不能被归为 %v: %v", label, target, err)
		}
	}
	if got.ID != "" || got.Name != "" {
		t.Fatalf("%s: 失败必须返回空采样点，实际为 %+v", label, got)
	}
}

// 非法 UTF-8 出现在编号或名称时整次拒绝；末尾分别带孤立 0xFF、0xFE 的两个
// 不同编号都不能登记成功，避免保存后被替换成同一个编号。即使另一项为空白，
// 也必须报告编码问题而不是空字段错误。
func TestRegisterPointRejectsInvalidUTF8(t *testing.T) {
	s, dir := open(t)

	bad := []string{
		"P\xff",        // 孤立 0xFF
		"P\xfe",        // 孤立 0xFE
		"P\xe4\xb8",    // 不完整的三字节序列
		"P\xc0\xaf",    // 非法多字节序列
		"取水口" + "\xff", // 合法中文后接孤立 0xFF
	}

	// 编号非法
	rejectPoint(t, s, "编号含孤立 0xFF", bad[0], "取水口")
	rejectPoint(t, s, "编号含孤立 0xFE", bad[1], "取水口")
	rejectPoint(t, s, "编号含不完整序列", bad[2], "取水口")
	rejectPoint(t, s, "编号含非法多字节序列", bad[3], "取水口")
	// 名称非法，包括开头不完整序列
	rejectPoint(t, s, "名称含孤立 0xFF", "P-new-1", bad[4])
	rejectPoint(t, s, "名称含孤立 0xFE", "P-new-2", "取水口"+"\xfe")
	rejectPoint(t, s, "名称开头不完整序列", "P-new-3", "\xe4\xb8"+"取水口")
	// 另一项为空白（去首尾空白后为空）仍报告编码错误
	rejectPoint(t, s, "非法编号+空白名称", bad[0], "   ")
	rejectPoint(t, s, "空白编号+非法名称", "   ", "名称"+"\xff")
	rejectPoint(t, s, "非法编号+非法名称", bad[0], "名称"+"\xfe")

	// 全部拒绝之后：没有任何采样点留下，编号没有被占用
	if len(s.points) != 0 {
		t.Fatalf("被拒绝的登记不得留下采样点，points=%v", s.points)
	}
	for _, id := range []string{"P-new-1", "P-new-2", "P-new-3"} {
		if _, err := s.SetLimit(id, "pH", 5, at(1, 0)); !errors.Is(err, ErrUnknownPoint) {
			t.Fatalf("被拒绝编号 %s 不应视为已登记，got %v", id, err)
		}
	}
	// 全部拒绝之后：数据文件根本不应生成（没有任何成功变更落盘）；
	// 若已存在，也不能含有替换字节
	if data, err := os.ReadFile(filepath.Join(dir, "water-data.json")); err == nil {
		if bytes.Contains(data, []byte{0xEF, 0xBF, 0xBD}) {
			t.Fatalf("被拒绝的非法文本被替换成 U+FFFD 落盘")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("读取落盘文件: %v", err)
	}

	// 先前失败占用过的编号改成合法名称后应能正常首次登记
	p, err := s.RegisterPoint("P-new-2", "  取水口  ")
	if err != nil {
		t.Fatalf("失败登记不得占用编号: %v", err)
	}
	if p.ID != "P-new-2" || p.Name != "取水口" {
		t.Fatalf("合法登记返回内容异常: %+v", p)
	}
}

// 已有合法采样点遭遇非法文本登记：必须直接失败，原名称保持不变，
// 已登记的限值、已确认样品及其判定依据仍归属原采样点；关闭后重新打开
// 同一数据目录，也不能出现由失败登记产生或改写的采样点。
func TestRegisterPointInvalidUTF8DoesNotTouchExistingPoint(t *testing.T) {
	s, dir := open(t)
	mustPoint(t, s, "P1", "取水口")
	mustLimit(t, s, "P1", "pH", 8.0, at(1, 0))
	mustSample(t, s, "S1", "P1", at(10, 0), Measurement{Item: "pH", Value: 9})
	confirmed := mustConfirm(t, s, "S1")
	if len(confirmed.Results) != 1 || !confirmed.Results[0].Exceeded || confirmed.Results[0].Limit != 8.0 {
		t.Fatalf("前置判定结果异常: %+v", confirmed)
	}

	// 用已有编号提交含非法字节的新名称：编码错误优先于重复编号
	rejectPoint(t, s, "已有编号+非法名称", "P1", "排水口"+"\xff")
	// 已有编号 + 空白名称 + 非法字节仍报告编码问题
	rejectPoint(t, s, "已有编号+非法空白名称", "P1", "\xe4\xb8")
	// 与原名称仅末尾非法字节不同的编号也不能登记
	rejectPoint(t, s, "形近非法编号", "P"+"\xfe", "取水口")

	// 原采样点名称与关联记录完整不变
	if p := s.points["P1"]; p.ID != "P1" || p.Name != "取水口" {
		t.Fatalf("已有采样点名称被失败登记改变: %+v", p)
	}
	if len(s.points) != 1 {
		t.Fatalf("失败登记不得新增采样点，points=%v", s.points)
	}
	again := mustConfirm(t, s, "S1")
	r := again.Results[0]
	if r.Item != "pH" || r.Limit != 8.0 || !r.LimitEffective.Equal(at(1, 0)) || !r.Exceeded {
		t.Fatalf("已保存的判定依据被失败登记改变: %+v", r)
	}
	list, err := s.ListByPoint("P1")
	if err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("按采样点查看记录变化: %+v err=%v", list, err)
	}

	// 重新打开后：仍是原采样点、原名称，失败登记没有留下任何痕迹
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertNoReplacementBytes(t, dir, "重开前")
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if len(s2.points) != 1 {
		t.Fatalf("重开后采样点数量变化: %v", s2.points)
	}
	if p := s2.points["P1"]; p.ID != "P1" || p.Name != "取水口" {
		t.Fatalf("重开后原采样点被改写: %+v", p)
	}
	reopened, err := s2.ListByPoint("P1")
	if err != nil || len(reopened) != 1 {
		t.Fatalf("重开后关联样品变化: %+v err=%v", reopened, err)
	}
	if rr := reopened[0]; rr.ID != "S1" || len(rr.Results) != 1 || rr.Results[0].Limit != 8.0 || !rr.Exceeded {
		t.Fatalf("重开后判定依据变化: %+v", rr)
	}
	// 被拒绝的形近编号在重开后仍可用合法名称首次登记
	if p, err := s2.RegisterPoint("P", "排水口"); err != nil || p.ID != "P" || p.Name != "排水口" {
		t.Fatalf("失败登记不应占用形近编号: %+v err=%v", p, err)
	}
}

// 真正的“�”（U+FFFD）、中文、文本内部的 U+0000 与内部空白都是合法字符，
// 继续接受；只去首尾空白，不替换、不合并不同名称。登记成功后返回的编号和
// 名称与保存内容一致，处理后的编号仍可用于登记限值、录入样品和按点查看。
func TestRegisterPointAcceptsValidUnicodeText(t *testing.T) {
	s, dir := open(t)

	cases := []struct{ id, name string }{
		{"P-�-01", "浊�度取水口"}, // 真正的替换字符 U+FFFD
		{"P-中文", "一号取水口"},    // 中文
		{"P0", "a\x00b"},     // 内部 U+0000
		{"P1", "取 水 口"},      // 内部空白保留，首尾空白去除
	}
	for _, c := range cases {
		p, err := s.RegisterPoint("  "+c.id+"  ", "  "+c.name+"  ")
		if err != nil {
			t.Fatalf("合法文本 %q 不应被拒绝: %v", c.id, err)
		}
		if p.ID != c.id || p.Name != c.name {
			t.Fatalf("只应去掉首尾空白，实际为 %+v", p)
		}
	}

	// 返回的编号/名称与保存内容一致，且处理后的编号可继续使用
	p, err := s.RegisterPoint("P2", "备用口")
	if err != nil {
		t.Fatalf("RegisterPoint P2: %v", err)
	}
	mustLimit(t, s, p.ID, "pH", 8.0, at(1, 0))
	smp := mustSample(t, s, "S1", "  "+p.ID+"  ", at(10, 0), Measurement{Item: "pH", Value: 7})
	if smp.PointID != "P2" {
		t.Fatalf("样品应归属处理后的编号: %+v", smp)
	}
	if list, err := s.ListByPoint("P2"); err != nil || len(list) != 1 || list[0].ID != "S1" {
		t.Fatalf("应能按处理后的编号查看记录: %+v err=%v", list, err)
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
		got, ok := s2.points[c.id]
		if !ok || got.Name != c.name {
			t.Fatalf("采样点 %q 在保存数据中缺失或被改写: %+v", c.id, got)
		}
	}
}

// 已关闭的数据存放仍按原有规则拒绝登记，编码检查不改变这一点。
func TestRegisterPointInvalidUTF8OnClosedStore(t *testing.T) {
	s, _ := open(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.RegisterPoint("P\xff", "取水口"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭应返回 ErrClosed，got %v", err)
	}
	if _, err := s.RegisterPoint("P1", "取水口"+"\xfe"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭应返回 ErrClosed，got %v", err)
	}
	if _, err := s.RegisterPoint("P1", "取水口"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭应返回 ErrClosed，got %v", err)
	}
}
