// 这份程序对应 README“样品作废”一节的完整示例：
// 同一采样点的两份已确认样品，较早一份达标、较晚一份超标，作废前最近有效结果指向较晚那份；
// 为较晚样品填写非空原因作废后，按点列表仍保留它的测量值与逐项判定依据（仅供核对历史），
// 而最近有效结果退回到较早的达标样品；对已作废样品再次确认会被明确拒绝。
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/descikazuyq/water-sample/water"
)

const layout = "2006-01-02 15:04:05 Z07:00"

func must(action string, err error) {
	if err != nil {
		log.Fatalf("%s 失败: %v", action, err)
	}
}

func statusText(st water.Status) string {
	switch st {
	case water.StatusPending:
		return "待判定"
	case water.StatusConfirmed:
		return "已确认"
	case water.StatusVoided:
		return "已作废"
	default:
		return string(st)
	}
}

// printSamples 按“状态 / 作废原因 / 逐项判定依据”打印。
// 已作废记录里的超标标记和逐项判定是作废前保存的历史，只用于核对，不是当前有效结论。
func printSamples(samples []water.Sample) {
	for _, smp := range samples {
		fmt.Printf("  样品 %s | 采样点 %s | 采样时间 %s | 状态 %s | 超标标记 %v\n",
			smp.ID, smp.PointID, smp.SampledAt.UTC().Format(layout),
			statusText(smp.Status), smp.Exceeded)
		if smp.Status == water.StatusVoided {
			fmt.Printf("    作废原因：%s\n", smp.VoidReason)
		}
		if len(smp.Results) == 0 {
			fmt.Println("    逐项判定：无（还没有判定依据）")
			continue
		}
		for _, r := range smp.Results {
			verdict := "达标"
			if r.Exceeded {
				verdict = "超标"
			}
			fmt.Printf("    逐项判定：项目 %s | 测量值 %g | 适用上限 %g（%s 生效）| %s\n",
				r.Item, r.Value, r.Limit, r.LimitEffective.UTC().Format(layout), verdict)
		}
	}
}

func main() {
	dir := flag.String("dir", "", "本地数据目录（指定一个空目录，不存在会自动创建）")
	flag.Parse()
	if *dir == "" {
		log.Fatal("请用 -dir 指定一个空的本地数据目录，例如：go run ./demo-void -dir ./water-data-void-demo")
	}

	// 1) 打开调用方指定的本地数据目录（空目录会自动创建；关闭后可重新打开）。
	store, err := water.Open(*dir)
	must("打开数据存放", err)
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("关闭数据存放: %v", err)
		}
	}()

	const pointID = "P1"
	const earlyID = "S-20260905-01" // 较早采样，达标
	const lateID = "S-20260910-01"  // 较晚采样，pH 超标
	effective := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	earlyAt := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	lateAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	// 2) 登记采样点，并为两个项目各登记一版采样前已生效的上限。
	_, err = store.RegisterPoint(pointID, "一号取水口")
	must("登记采样点", err)
	_, err = store.SetLimit(pointID, "pH", 8.0, effective)
	must("登记 pH 上限", err)
	_, err = store.SetLimit(pointID, "浊度", 3.0, effective)
	must("登记浊度上限", err)

	// 3) 录入并确认较早的样品：pH 7、浊度 2，均在限值内，整份达标。
	_, err = store.SubmitSample(earlyID, pointID, earlyAt,
		water.Measurement{Item: "pH", Value: 7},
		water.Measurement{Item: "浊度", Value: 2},
	)
	must("录入较早样品", err)
	_, err = store.Confirm(earlyID)
	must("确认较早样品", err)

	// 4) 录入并确认较晚的样品：pH 9 大于上限 8，整份超标。
	_, err = store.SubmitSample(lateID, pointID, lateAt,
		water.Measurement{Item: "pH", Value: 9},
		water.Measurement{Item: "浊度", Value: 2},
	)
	must("录入较晚样品", err)
	_, err = store.Confirm(lateID)
	must("确认较晚样品", err)

	// 5) 作废前，最近有效结果是较晚采样的超标样品。
	latest, ok, err := store.LatestResult(pointID)
	must("作废前查询最近有效结果", err)
	if !ok {
		log.Fatal("作废前应有最近有效结果")
	}
	fmt.Println("作废前最近有效结果（指向较晚的超标样品）：")
	printSamples([]water.Sample{latest})

	// 6) 作废原因仅含空白时同样被拒绝，这不是成功操作。
	_, err = store.Void(lateID, "   ")
	if err == nil {
		log.Fatal("空白作废原因不应成功")
	}
	if !errors.Is(err, water.ErrEmptyField) {
		log.Fatalf("预期空字段错误，实际为: %v", err)
	}
	fmt.Printf("空白原因作废被拒绝：%v\n", err)

	// 7) 填写非空原因作废较晚的超标样品。作废取消它作为有效结果的资格，
	//    但不删除已保存的测量值与逐项判定依据。
	voided, err := store.Void(lateID, "采样瓶破损，等待复测重采")
	must("作废较晚样品", err)
	fmt.Println("作废成功，作废后的样品记录（判定依据保留，仅供核对历史）：")
	printSamples([]water.Sample{voided})

	// 8) 按采样点查看：两份样品都在，已作废的较晚样品仍带着
	//    作废前保存的测量值、所用上限、生效时间和逐项结论。
	list, err := store.ListByPoint(pointID)
	must("作废后按采样点查询", err)
	fmt.Println("作废后按采样点查询（较晚样品仍在列表中，历史依据保留）：")
	printSamples(list)

	// 9) 最近有效结果退回到较早的已确认达标样品：
	//    不能因为作废记录里仍有判定内容就继续选中它。
	latest, ok, err = store.LatestResult(pointID)
	must("作废后查询最近有效结果", err)
	if !ok {
		log.Fatal("较早样品仍已确认且未作废，应有最近有效结果")
	}
	fmt.Println("作废后最近有效结果（退回较早的达标样品）：")
	printSamples([]water.Sample{latest})
	if latest.ID != earlyID {
		log.Fatalf("最近有效结果应为 %s，实际为 %s", earlyID, latest.ID)
	}

	// 10) 对已作废样品再次请求确认，被明确拒绝。
	//     必须先处理错误：err != nil 时返回的是空样品，不能打印成确认成功。
	reconfirmed, err := store.Confirm(lateID)
	if err == nil {
		log.Fatal("已作废样品不应再确认成功")
	}
	if !errors.Is(err, water.ErrVoided) {
		log.Fatalf("预期已作废错误，实际为: %v", err)
	}
	fmt.Printf("对已作废样品再次确认被拒绝：%v\n", err)
	fmt.Printf("（出错时返回值是空样品：编号 %q，状态 %q，不能当作确认成功）\n",
		reconfirmed.ID, reconfirmed.Status)

	// 11) 拒绝不会改动记录：原作废状态、原因和历史判定依据继续保留。
	list, err = store.ListByPoint(pointID)
	must("拒绝后按采样点查询", err)
	fmt.Println("再次确认被拒绝后按采样点查询（作废状态、原因与历史依据不变）：")
	printSamples(list)

	// 12) 一个没有任何已确认且未作废样品的采样点，最近有效结果明确返回无结果。
	//     ok 为 false 才是“无结果”的表达方式；不能把空返回值或待判定记录里
	//     为 false 的超标标记解释成达标。
	_, err = store.RegisterPoint("P2", "二号取水口")
	must("登记第二个采样点", err)
	_, ok, err = store.LatestResult("P2")
	must("查询无结果采样点", err)
	fmt.Printf("采样点 P2 没有任何已确认样品：最近有效结果 ok=%v（明确表示无结果，不是达标）\n", ok)
}
