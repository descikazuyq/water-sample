// Command example 演示一份含两个测量项目的样品，因缺少采样当时适用的限值
// 而无法确认时，调用方应怎样补录限值并对原样品继续确认。
//
// 用法：
//
//	go run ./example <空数据目录>
//
// 数据目录由调用方指定，不存在会自动创建；建议首次运行传入一个空目录，
// 重复运行请换新目录，否则已登记的采样点会导致重复登记错误。
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/descikazuyq/water-sample/water"
)

const layout = "2006-01-02 15:04 MST"

func main() {
	if len(os.Args) != 2 {
		log.Fatal("用法: go run ./example <空数据目录>")
	}
	dir := os.Args[1]

	// 打开（必要时创建）调用方指定的本地数据目录。
	store, err := water.Open(dir)
	if err != nil {
		log.Fatalf("打开数据目录失败: %v", err)
	}
	defer func() {
		// 关闭后记录已落盘；用同一目录重新 water.Open 仍能查到这份样品。
		if err := store.Close(); err != nil {
			log.Fatalf("关闭数据存放失败: %v", err)
		}
	}()

	// 所有时间明确写出，统一使用 UTC。
	sampledAt := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)   // 采样时间
	phLimitAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)    // pH 上限：采样之前已生效
	codFutureAt := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC) // COD 仅有采样之后才生效的版本

	// 登记采样点。
	if _, err := store.RegisterPoint("P1", "取水口"); err != nil {
		log.Fatalf("登记采样点失败: %v", err)
	}
	// pH：上限 8.0，9 月 1 日起生效，9 月 10 日采样时适用。
	if _, err := store.SetLimit("P1", "pH", 8.0, phLimitAt); err != nil {
		log.Fatalf("登记 pH 上限失败: %v", err)
	}
	// COD：只登记了 9 月 20 日才生效的版本，9 月 10 日采样时它尚未生效。
	// 存在“未来版本”不代表当前样品能使用它。
	if _, err := store.SetLimit("P1", "COD", 40.0, codFutureAt); err != nil {
		log.Fatalf("登记 COD 上限失败: %v", err)
	}

	// 录入同一份样品，含两个测量项目：pH=9，COD=30。初始状态为待判定。
	smp, err := store.SubmitSample("S1", "P1", sampledAt,
		water.Measurement{Item: "pH", Value: 9},
		water.Measurement{Item: "COD", Value: 30},
	)
	if err != nil {
		log.Fatalf("录入样品失败: %v", err)
	}
	fmt.Printf("样品 %s 已录入，初始状态：%s\n", smp.ID, statusText(smp.Status))

	// 第一次确认：COD 在采样时刻没有适用上限，整次确认被拒绝。
	first, err := store.Confirm("S1")
	if err == nil {
		log.Fatal("缺少适用上限时确认应当被拒绝")
	}
	// 必须用 errors.Is 识别缺少适用限值，不能忽略 err 把返回的零值样品当成功结果。
	if !errors.Is(err, water.ErrMissingLimit) {
		log.Fatalf("确认失败，但原因不是缺少适用上限: %v", err)
	}
	fmt.Printf("第一次确认被拒绝：%v\n", err)
	fmt.Printf("  COD 只有 %s 才生效的上限，晚于采样时间 %s，采样当时不适用\n",
		codFutureAt.Format(layout), sampledAt.Format(layout))
	fmt.Printf("  被拒绝时返回样品的编号为 %q：它是零值，不能当作成功结果\n", first.ID)

	// 失败后按采样点查询：样品仍是待判定，没有单项结果，也没有整份结论。
	list, err := store.ListByPoint("P1")
	if err != nil {
		log.Fatalf("按点查询失败: %v", err)
	}
	printSample(list[0], "失败后按采样点 P1 查询")
	// 待判定样品不会成为“最近有效结果”。
	if latest, ok, err := store.LatestResult("P1"); err != nil {
		log.Fatalf("查询最近有效结果失败: %v", err)
	} else if ok {
		log.Fatalf("确认失败时不应有最近有效结果，却得到 %s", latest.ID)
	} else {
		fmt.Println("最近有效结果：无（待判定样品不算有效结果）")
	}

	// 补录一版 COD 上限：生效时间恰好等于采样时间（不晚于采样时刻即适用）。
	codBackfillAt := sampledAt
	if _, err := store.SetLimit("P1", "COD", 30.0, codBackfillAt); err != nil {
		log.Fatalf("补录 COD 上限失败: %v", err)
	}
	fmt.Printf("\n已补录 COD 上限 30，生效时间 %s（恰好等于采样时间，该版本被采用）\n",
		codBackfillAt.Format(layout))

	// 对原样品再次确认（样品编号不变，不需要重新录入）。
	confirmed, err := store.Confirm("S1")
	if err != nil {
		log.Fatalf("补录后再次确认失败: %v", err)
	}
	fmt.Println("对原样品 S1 再次确认：")
	for _, r := range confirmed.Results {
		fmt.Printf("  %s：测量值 %g，所用上限 %g（%s 生效），%s\n",
			r.Item, r.Value, r.Limit, r.LimitEffective.Format(layout), exceedText(r.Exceeded))
	}
	fmt.Printf("整份样品：%s（任一项目超标即整份超标）\n", exceedText(confirmed.Exceeded))

	// 成功后再按同一采样点查询：记录已带上逐项判定依据，状态为已确认。
	list2, err := store.ListByPoint("P1")
	if err != nil {
		log.Fatalf("补录后按点查询失败: %v", err)
	}
	printSample(list2[0], "成功后按采样点 P1 查询")
	latest, ok, err := store.LatestResult("P1")
	if err != nil {
		log.Fatalf("查询最近有效结果失败: %v", err)
	}
	if !ok || latest.ID != "S1" {
		log.Fatalf("最近有效结果应指向 S1，ok=%v", ok)
	}
	fmt.Printf("最近有效结果：%s\n", latest.ID)
}

func printSample(s water.Sample, label string) {
	fmt.Printf("--- %s ---\n", label)
	fmt.Printf("样品 %s，采样点 %s，采样于 %s，状态：%s\n",
		s.ID, s.PointID, s.SampledAt.Format(layout), statusText(s.Status))
	for _, m := range s.Measurements {
		fmt.Printf("  测量 %s = %g\n", m.Item, m.Value)
	}
	if s.Status != water.StatusConfirmed {
		fmt.Printf("  判定依据：无（Results 为空，未算出任何单项结果）\n")
		fmt.Printf("  Exceeded=%v 只是零值默认值：缺少判定依据时没有结论，不表示达标\n", s.Exceeded)
		return
	}
	for _, r := range s.Results {
		fmt.Printf("  判定 %s：测量值 %g，所用上限 %g（%s 生效），%s\n",
			r.Item, r.Value, r.Limit, r.LimitEffective.Format(layout), exceedText(r.Exceeded))
	}
	fmt.Printf("  整份样品：%s\n", exceedText(s.Exceeded))
}

func statusText(st water.Status) string {
	switch st {
	case water.StatusPending:
		return "pending（待判定）"
	case water.StatusConfirmed:
		return "confirmed（已确认）"
	case water.StatusVoided:
		return "voided（已作废）"
	default:
		return string(st)
	}
}

func exceedText(exceeded bool) string {
	if exceeded {
		return "超标"
	}
	return "达标"
}
