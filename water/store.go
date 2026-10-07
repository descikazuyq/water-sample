package water

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Status 表示样品所处的判定状态。
type Status string

const (
	// StatusPending 样品已录入，尚未确认判定结果。
	StatusPending Status = "pending"
	// StatusConfirmed 样品已确认，判定结果已保存。
	StatusConfirmed Status = "confirmed"
	// StatusVoided 样品已作废，保留记录但不能再确认。
	StatusVoided Status = "voided"
)

var (
	ErrClosed             = errors.New("water: 数据存放已关闭")
	ErrEmptyField         = errors.New("water: 编号、名称或项目去掉首尾空白后不能为空")
	ErrDuplicatePoint     = errors.New("water: 采样点编号已登记")
	ErrUnknownPoint       = errors.New("water: 采样点未登记")
	ErrInvalidValue       = errors.New("water: 数值只接受有限数")
	ErrInvalidTime        = errors.New("water: 采样时间或生效时间缺失")
	ErrDuplicateLimitTime = errors.New("water: 同一采样点同一项目已存在相同生效时间的上限")
	ErrUnknownSample      = errors.New("water: 样品编号不存在")
	ErrSampleConflict     = errors.New("water: 同一编号提交的样品内容不一致")
	ErrDuplicateItem      = errors.New("water: 同一份样品的测量项目重复")
	ErrNoMeasurements     = errors.New("water: 样品至少需要一个项目的测量值")
	ErrMissingLimit       = errors.New("water: 存在找不到适用上限的测量项目")
	ErrVoided             = errors.New("water: 样品已作废，不能再确认")
	ErrVoidReasonConflict = errors.New("water: 作废原因与已有记录不一致")
	ErrInvalidText        = errors.New("water: 采样点编号、名称、项目名、样品编号或作废原因不是合法的 UTF-8 文本")
	ErrCorruptRecord      = errors.New("water: 本地数据中存在缺少状态或状态值不受支持、缺少采样时间、缺少采样点编号、采样点编号含首尾空白或采样点未登记、缺少原测量、原测量或逐项判定缺少测量值、测量项目重复、测量项目与逐项判定对应不上、逐项判定缺少上限数值、逐项判定缺少上限生效时间或生效时间晚于采样时间、超标标记与保存的判定依据不一致的损坏样品记录，样品记录缺少编号、编号含首尾空白或集合编号与记录自身编号不一致的损坏样品记录，样品集合中样品编号重复的损坏数据，或缺少采样点编号、采样点编号含首尾空白或采样点未登记、缺少上限数值、缺少生效时间的损坏限值记录")
)

// SamplingPoint 是按编号唯一登记的采样点。
type SamplingPoint struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Limit 是某采样点某测量项目的一版数值上限。
type Limit struct {
	PointID   string    `json:"pointId"`
	Item      string    `json:"item"`
	Value     float64   `json:"value"`
	Effective time.Time `json:"effective"`
}

// Measurement 是样品中一个项目的测量值。
type Measurement struct {
	Item  string  `json:"item"`
	Value float64 `json:"value"`
}

// ItemResult 是确认后单个项目的判定结果。
type ItemResult struct {
	Item           string    `json:"item"`
	Value          float64   `json:"value"`
	Limit          float64   `json:"limit"`
	LimitEffective time.Time `json:"limitEffective"`
	Exceeded       bool      `json:"exceeded"`
}

// Sample 是一份从录入、确认到作废的样品记录。
type Sample struct {
	ID           string        `json:"id"`
	PointID      string        `json:"pointId"`
	SampledAt    time.Time     `json:"sampledAt"`
	Measurements []Measurement `json:"measurements"`
	Status       Status        `json:"status"`
	Results      []ItemResult  `json:"results,omitempty"`
	Exceeded     bool          `json:"exceeded"`
	VoidReason   string        `json:"voidReason,omitempty"`
}

// diskState 是落盘的完整数据。
type diskState struct {
	Points  map[string]SamplingPoint `json:"points"`
	Limits  map[string][]Limit       `json:"limits"` // 键仅为落盘占位，读取时按每条限值自身的采样点与项目重新分组
	Samples map[string]*Sample       `json:"samples"`
}

// limitGroup 用两个字段各自标识一组限值，避免把包含 U+0000 的文本
// 用单字符拼接成键时不同组合撞成同一组。
type limitGroup struct {
	pointID string
	item    string
}

// Store 是绑定到某个本地目录的取样判定数据存放。
// 不同目录的数据互不混用；关闭后重新打开仍能查看原有记录。
type Store struct {
	mu      sync.Mutex
	dir     string
	file    string
	closed  bool
	points  map[string]SamplingPoint
	limits  map[limitGroup][]Limit // 按生效时间升序
	samples map[string]*Sample
}

// Open 打开（必要时创建）dir 下的本地数据存放。
//
// 读入时先核对每份样品都明确写有三种状态之一：pending（待判定）、confirmed
// （已确认）、voided（已作废）。状态决定样品能否产生有效结论，原测量完整、
// 逐项判定齐全或作废原因仍在都不能代替明确的状态。status 字段缺失、保存为
// null 或为空字符串都是状态缺失；任何其他字符串——包括带首尾空白或大小写
// 不同的写法——都是不支持的状态值，不能自动整理后接受，也不能据测量值、
// 判定依据或作废原因猜出它原来是否作废。发现上述问题时整次打开以可被
// errors.Is(err, ErrCorruptRecord) 识别的错误失败，返回 nil 数据存放，
// 错误信息点到具体样品编号，并区分状态缺失与不支持的状态值（未知字符串在
// 说明中原样带出）；同一文件中其他样品正常也不只读入正常部分，打开失败不
// 改写原文件、不补状态、不产生或替换结论。
//
// 状态合法后还要核对每份样品都保留采样时间：sampledAt 字段缺失、保存为 null
// 或明确保存为 Go 零时间 0001-01-01T00:00:00Z，反序列化后都是零时间，一律视为
// 缺少采样时间，不能解释成“最早时刻”，也不能补成当前时间或从所用上限的生效
// 时间推测采样日期。这条要求适用于待判定、已确认和已作废的全部样品：作废前是否
// 确认过、是否仍保留逐项判定，都不影响采样时间必须存在；测量项目和数值完整，
// 或者已经保留相互一致的达标、超标依据，也不能代替日期。核对在逐项判定的上限
// 生效时间比较之前：采样时间缺失的已确认记录必须明确报告日期缺失，而不是只说
// 某项上限晚于采样时间。只要文件里有一份样品缺少采样时间，本次打开就整体失败，
// 返回 nil 数据存放，错误信息点到具体样品编号，并明确说明缺少的是样品的采样
// 时间，使调用方能与登记限值或判定依据中缺少生效时间的问题区分；同一文件中
// 其他样品正常也不能只读入正常部分。拒绝读取时保留原文件，不补日期、不从所用
// 上限的生效时间推测采样日期，也不改变样品状态或已有结论。完整日期继续按真实
// 时刻使用，不增加日期早晚范围限制、不截掉小数秒，按点列表与最近有效结果的先后
// 关系保持不变。
//
// 状态合法后再核对每份样品都至少保留一个原测量项目：Measurements 为空列表、保存为
// null 或整个字段缺失都算样品内容残缺，即使编号、采样点、采样时间齐全也不能
// 接受；这条要求对任何状态的样品都生效，包括待判定样品，以及待判定后直接作废、
// 不再参与有效判定的样品——不能因为它没有历史结论就放过内容丢失；原测量为空但
// 残留逐项判定的记录也不能反过来用判定内容填补原测量。再核对每个原测量项目的
// 测量值：value 字段缺失或保存为 null 都是内容残缺，即使读出的零值与另一处
// 明确保存的零恰好一致、或按零与上限比较能得到已保存的达标标记，也不能当成
// 合法数值接受；这条要求同样对任何状态的样品生效，多项目样品只缺一个项目的
// 数值也不能只接收其余项目。明确保存的数值零仍是合法测量值，正数、负数沿用
// 现有规则。再核对原测量项目名：
// 同一个测量项目名在一份样品里只能出现一次，不依赖两条记录是否相邻，也不因
// 数值相同（包括两个零）而放行。再核对每份已保存结论的样品：已确认样品，以及
// 已确认后作废、逐项依据仍保留在 Results 中的样品，其原测量项目与逐项判定
// 必须按项目名完整一一对应；判定里同一项目不能出现两次；每个测量项目必须且
// 只能有一条判定，判定里也不能出现原测量没有的项目；对应项目的判定测量值必须
// 与原测量值一致（零是合法测量值，不会被当成缺项）。每条逐项判定还必须实际
// 保存了测量值：value 字段缺失或保存为 null 都是判定内容残缺，即使读出的零值
// 与原测量明确保存的零一致、或与上限比较能得到已保存的达标标记，也不能当成
// 合法结论；明确保存数值零的判定测量值不在此列。每条逐项判定还必须实际
// 保存了上限数值：limit 字段缺失或保存为 null 都是判定依据残缺，即使读出
// 的零值与测量值、超标标记恰好对得上，也不能当成“零等于零”的合法结论；
// 明确保存数值零的上限仍是合法依据，负数和正数上限也保持原有行为。每条逐项
// 判定保存的上限生效时间也必须存在且不晚于这份样品的采样时间：limitEffective
// 字段缺失、保存为 null 或为 Go 零时间都表示缺少生效依据，不能当成一版很早的
// 上限放行；生效时间晚于采样时间（按真实时刻比较，保留纳秒精度，恰好相等可以
// 接受，晚一纳秒也拒绝；不同时区写法表示同一时刻按相等处理）说明采样时该上限
// 尚未生效，即使测量值、上限数值与超标标记互相对得上也必须拒绝。两个列表
// 排列顺序不同不影响对应，成功读入后测量与判定各自保留原顺序。最后核对超标
// 标记与该样品已保存的判定依据一致：每条判定的超标标记必须严格符合该条保存
// 的测量值与上限——测量值严格大于上限才是超标，小于或等于（包括等于、零与
// 负数的合法组合）都应为达标；整份样品的超标标记必须与逐项结论一致，任一
// 项超标就应为真、全部达标就应为假。只核对本文件里已保存的依据，不重新选择
// 当前限值、不重做判定。任一份样品对不上，整次打开以可被
// errors.Is(err, ErrCorruptRecord) 识别的错误失败，返回 nil 数据存放，
// 错误信息点到具体样品编号与缺少原测量的原因（或具体项目）；原测量或逐项判定
// 缺少测量值时点到具体项目，并说明缺少的是原测量值还是逐项判定中的测量值；判定缺少上限
// 数值时点到具体项目；判定缺少上限生效时间或生效时间晚于采样时间时点到具体
// 项目，晚于采样时间时同时带出两处时间（保留足以说明先后的小数秒）；单项超标标记有误时点到具体项目，整份标记有误时说明
// 它与逐项结论不一致；不会静默跳过问题样品、不会择一保留或合并重复测量、
// 不会补出缺失测量、测量值、判定、上限数值或生效时间、不会修正超标标记或重新判定、不会把它
// 改成另一种状态、也不会把拒绝推迟到请求确认时，更不会改写原文件。待判定
// 样品没有判定记录、待判定后作废的样品没有历史依据，均属正常，只要原测量
// 完整就照常读入，不要求它们提前保存判定上限。
// 完整记录原样读入，已保存的上限、生效时间、逐项结论与整份超标标记保留，不因
// 后来新增或补录的限值而拒绝或重新计算。没有任何样品的新数据目录，以及样品
// 集合为空的已有数据，都照常打开。
//
// 在状态与采样时间等逐份校验之前，还要先核对每份样品的编号本身：这份记录在
// samples 集合中的集合编号，必须与记录自身 id 字段写出的编号逐字一致，并沿用
// 正常录入后保存的编号形式。编号是按点查看、确认与作废时定位样品的唯一依据，
// 若集合里写成 S1、记录里却写成 S2，打开后所有查询都会按 S2 走：可能得到
// “样品不存在”，也可能误操作另一份真正编号为 S2 的记录。id 字段缺失、保存为
// null、为空字符串或仅含空白，都算这份记录缺少编号；集合编号或记录 id 任一处
// 带首尾空白，也不能去掉空白后放行；两处文本都完整但对不上，属于两处编号不
// 一致。完整编号按 JSON 解码后的文本比较，不转换大小写、不替换字符：一处直接
// 写 S1、另一处用 Unicode 转义表示同一文本可以接受；S1 与 s1、S1 与 S2 则不
// 一致，中文与编号内部的合法字符仍可使用。发现上述任一问题时整次打开以可被
// errors.Is(err, ErrCorruptRecord) 识别的错误失败，返回 nil 数据存放，错误信息
// 指出集合中的编号，并区分记录缺少编号、编号含首尾空白和两处编号不一致；不
// 一致时同时带出两处编号，让调用方知道是哪条记录对不上。这条要求适用于待
// 判定、已确认和已作废的全部样品：测量与判定依据完整不能代替编号正确；同一
// 文件里其他样品正常也不能只读入正常部分。拒绝读取时保留原文件，不补编号、
// 不用集合编号回填、不移动或合并条目，不改变状态或重新判定。正常录入保存的
// 数据两处编号始终相同，照常打开。
//
// 在逐份校验之前还要先核对样品集合本身：每个样品编号在整份文件的所有样品
// 集合中只能出现一次。JSON 反序列化进 map 时同键的后一条记录会静默覆盖前一
// 条，若不在读取时单独核对，原本已确认的超标结论可能被另一条同编号的达标
// 结论替换，而所有逐份完整性检查都只能看到幸存条目、发现不了被覆盖的记录。
// 文件顶层写出多个 samples 对象时反序列化会把它们合并进同一个 map，同一编号
// 分散在不同对象里同样只剩幸存条目，因此唯一性核对覆盖所有会被当作样品集合
// 读取的条目：同一编号无论出现在同一个集合还是不同集合、两次出现之间是否
// 隔着其他样品或顶层字段，都在打开时整体拒绝；字段名按反序列化识别样品集合
// 的同一规则匹配，Samples 等大小写写法同样纳入核对，不能借换大小写绕过；
// 后面再出现空集合或 null 也不能让前面集合里已经出现的重复编号逃过检查。
// 编号是否相同按 JSON 解码后的完整文本判断：同一个 S1 一处直接写出、另一处
// 把 S 写成 Unicode 转义，仍然是同一编号；重复条目之间隔着其他样品也一样，
// 不依赖两条是否相邻。即使两条记录内容完全相同也按重复编号拒绝，不按排列
// 顺序选出其中一条作为“最近”结果；这条要求适用于待判定、已确认和已作废的
// 全部样品，不以是否已有判定结果为条件。发现重复编号时整次打开以可被
// errors.Is(err, ErrCorruptRecord) 识别的错误失败，返回 nil 数据存放，
// 错误信息指出重复的样品编号并说明样品集合中存在重复编号，使调用方能把它与
// 同一份样品内部测量项目重复的问题区分开；同一文件里其他样品再正常也不只
// 读入正常部分，不删除条目、不合并内容、不择一保留、不补出判定、不重算历史
// 结论，也不回写原文件。多个非空样品集合没有重复编号、其余内容合法时沿用
// 现有读取行为，不因集合字段出现多次而拒绝；样品集合为空或缺失照常打开。
// 不同样品都包含同一测量项目（如各自都有 pH）或同名字段是正常数据，不属于
// 样品编号重复。
//
// 每份样品还必须明确属于一个已经登记的采样点：正常录入时采样点必须先登记，
// 打开已有本地数据时也要按同一规则核对样品归属，不能因为其余内容通过检查就
// 接受归属不明的记录——否则文件只登记了 P1、样品却写着 P9 时，已确认记录
// 甚至能作为 P9 的最近有效结果返回。pointId 字段缺失、保存为 null、为空字符串
// 或仅含空白，都按缺少采样点编号处理；带首尾空白的编号也属于损坏，不能去掉
// 空白后替它找到一个采样点。完整编号按 JSON 解码后的文本与已登记编号逐字
// 匹配，不转换大小写：P1 与 p1 是不同编号；一处直接写 P1、另一处用 Unicode
// 转义表示同一文本则相同，中文编号同理。编号完整但没有对应登记记录时，明确
// 说明该采样点未登记，不能从限值记录或其他样品推测它存在。这条核对适用于
// 待判定、已确认和已作废样品：作废记录仍供按点核对，不能放过丢失的归属；
// 已确认样品的判定依据完整，也不能代替采样点已经登记。只要一份样品不符合
// 要求，整次 Open 就失败，返回 nil 数据存放，错误可被
// errors.Is(err, ErrCorruptRecord) 识别；错误信息指出样品编号，并区分编号
// 缺失、含首尾空白和采样点未登记，后两种情况带出原采样点编号，便于找出对应
// 记录。其他样品正常也不能只读入正常部分；失败时保留原文件，不补登记采样点、
// 不改写样品归属或已有结论。归属完整的记录继续按现有规则读取，原状态、测量
// 与历史判定依据照常保留，按点列表及最近有效结果的排序和作废资格不变。采样点
// 已登记但尚未登记限值的待判定样品仍可打开，是否缺少采样当时适用的上限继续由
// 确认操作判断；没有样品的空目录和空样品集合也照常打开。
//
// 每一版登记限值也必须明确属于一个已经登记的采样点：沿用“登记上限前必须先
// 登记采样点”的规则，打开已有本地数据时按同一规则核对限值归属，不能因为数值
// 与生效时间完整就接受归属不明的上限——否则文件只登记了 P1、却保存着一版
// 归属 P9 的 pH 上限时，这版上限会被读入，后来登记 P9 并确认样品还会用上
// 这条原本无法通过正常登记入口写入的限值。核对对象是每一版限值记录自身保存的
// pointId（按 JSON 解码后的文本），不是保存这组限值的 limits 集合键；含
// U+0000 的不同组合即使共用旧版集合键，也只按各自记录的编号核对。pointId
// 字段缺失、保存为 null、为空字符串或仅含空白，都算这版限值缺少采样点编号；
// 带首尾空白的编号也属于损坏，不能去掉空白后替它匹配。完整编号按读取后的文本
// 与本文件已登记的采样点编号逐字比较，不转换大小写：P1 与 p1 不同；一处直接
// 写出、另一处用 Unicode 转义表示同一文本可以匹配，中文编号同理。编号完整却
// 找不到登记记录时，明确说明该采样点未登记，不能凭限值或样品反过来补登记。
// 这条核对针对文件中实际存在的每一版限值，与当前有没有样品、这一版是否已被
// 样品采用无关：只有旧版或未来版本归属有问题也一样拒绝，文件尚未录入任何样品
// 时也不例外。任一版不符合要求，整次 Open 都失败，返回 nil 数据存放，错误可
// 被 errors.Is(err, ErrCorruptRecord) 识别；错误说明指出这是登记限值的归属
// 问题、点到对应测量项目与生效时间，并区分编号缺失、含首尾空白和未登记，后两
// 种情况原样带出该版保存的编号。其他限值与样品再正常也不能只读入正常部分；
// 失败时保留原文件，不补登记采样点、不改写限值归属，也不把拒绝推迟到确认某份
// 样品时。若样品和限值同时引用未登记采样点，仍沿用现有的样品归属错误（保留
// 样品编号与未登记说明）。归属完整的限值继续按各自的采样点和项目分组，共用
// 旧版集合键的不同组合以及编号内部含 U+0000 的合法记录仍可读取；没有限值的
// 正常数据照常打开，待判定样品是否缺少适用上限仍在确认时判断，已有完整样品的
// 状态与历史判定依据保留，不因这项核对重新计算结论。
//
// 限值版本同样在打开时核对：同一采样点、同一项目（归属只看每条记录自身的
// pointId 与 item，不看落盘键，含 U+0000 的不同组合即使共用旧版存储键也各自
// 成组）下只要存在两条生效时刻相同的上限，整次打开即以可被
// errors.Is(err, ErrDuplicateLimitTime) 识别的错误失败，返回 nil 数据存放，
// 错误信息点到具体采样点、项目与重复的生效时间。即使两条数值完全相同也算
// 重复：不合并、不择一保留、不跳过问题组只读入其余部分，文件里其他采样点与
// 样品再正常也不部分读入，更不回写原文件或把拒绝推迟到确认某份样品时。
// 生效时刻按真实时刻判断：不同时区偏移但表示同一瞬间的日期仍算重复，相差一
// 纳秒的两版是合法的不同版本，不按秒截断。不同采样点或不同项目在同一时刻
// 各登记一版仍合法。没有重复的多版本数据可乱序保存后正常打开，确认时继续
// 采用采样当时已生效的最近一版，恰好等于生效时刻采用新版；已确认或已作废
// 样品保存的历史判定依据原样保留，不因读取检查重算。
//
// 每一版登记限值还必须在文件中实际保存上限数值：value 字段缺失或保存为
// null 时，JSON 反序列化会把它落成 float64 零值，与明确保存的零上限无法
// 区分，只能在读取时按字段是否存在核对。这条核对针对文件中实际存在的每一
// 版限值，与当前有没有样品、这一版是否已被样品采用无关：同一项目的旧版或
// 尚未生效的版本缺少数值同样拒绝，不能只检查最新版本；文件尚未录入任何
// 样品时也一样。任一版缺少数值，整次打开即以可被
// errors.Is(err, ErrCorruptRecord) 识别的错误失败，返回 nil 数据存放，
// 错误信息点到该限值自身的采样点编号、项目与生效时间，并明确说明缺少上限
// 数值；不补成零、不跳过这一版、不借用同项目另一版的数值，不把拒绝推迟到
// 确认某份样品时，也不回写原文件。明确保存的数值零仍是合法上限，负数和
// 正数保持现有含义：适用上限确实为零时，测量值为零达标、大于零超标。没有
// 登记限值的空数据照常打开，后续样品缺少适用上限时仍按原有行为拒绝确认。
//
// 每一版登记限值还必须写明从什么时候生效：effective 字段缺失、保存为 null
// 或明确写成 Go 零时间 0001-01-01T00:00:00Z，反序列化后都是零时间，一律视为
// 缺少生效时间，不能把它解释成一版很早以前就生效的上限，即使采样点、项目与
// 上限数值完整也不能放行。这条核对针对文件中实际存在的每一版，与当前有没有
// 样品、这一版是否已被样品采用、同组是否另有完整版本无关：同一项目有一版
// 完整限值也不能遮住另一版缺日期的问题；旧版或尚未到生效时刻的版本缺少
// 生效时间同样拒绝，不能只检查最新版本；文件尚未录入任何样品时也一样。
// 任一版缺少生效时间，整次打开即以可被 errors.Is(err, ErrCorruptRecord)
// 识别的错误失败，返回 nil 数据存放，错误信息点到该限值自身的采样点编号与
// 项目名，并明确说明缺少的是登记限值的生效时间；不补日期、不借用同项目另一
// 版的日期、不跳过这一版、不重新计算或改写样品结论，不把拒绝推迟到确认某份
// 样品时，也不回写原文件。没有登记限值的空数据照常打开；生效时间完整的未来
// 版本不是损坏，照常读入，确认时仍只选采样当时已生效的最近一版。
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("%w: 数据目录", ErrEmptyField)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:     dir,
		file:    filepath.Join(dir, "water-data.json"),
		points:  map[string]SamplingPoint{},
		limits:  map[limitGroup][]Limit{},
		samples: map[string]*Sample{},
	}
	data, err := os.ReadFile(s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var st diskState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("water: 读取数据文件失败: %w", err)
	}
	// 样品集合这一层先于任何逐份校验：JSON 反序列化进 map 时，同键的后一条
	// 记录会悄悄覆盖前一条，已确认的超标结论可能被另一条同编号的达标结论替换，而
	// 逐份完整性校验只能看到幸存下来的条目。按文件中的原始 JSON 标记核对所有
	// 会被当作样品集合读取的条目（含 Samples 等大小写写法），每个样品编号在
	// 整份文件中只能出现一次；发现重复时整次打开失败，不返回数据存放对象，
	// 不删除、不合并、不择一、不补判定、不重算历史结论，也不回写原文件。
	// 核对不以反序列化后的 st.Samples 非空为条件：后面再出现空集合或 null 会
	// 把 st.Samples 落回 nil，但前面集合里已经出现的重复编号不能因此逃过检查。
	dupID, err := duplicateSampleID(data)
	if err != nil {
		return nil, fmt.Errorf("water: 读取数据文件失败: %w", err)
	}
	if dupID != "" {
		return nil, fmt.Errorf("%w: 样品集合中存在重复的样品编号 %s：样品编号在本地样品集合中只能出现一次；这是样品集合内编号重复，不同于同一份样品内部的测量项目重复",
			ErrCorruptRecord, dupID)
	}
	if st.Points != nil {
		s.points = st.Points
	}
	// JSON 反序列化把数值字段缺失与 null 都落成 float64 零值，与明确保存的零
	// 无法区分，因此“有没有保存数值”只能在读取时按字段是否存在单独核对：
	// 登记限值的 value、样品的原测量 value、逐项判定的 value 与 limit 都靠
	// 这一次重新扫描探出。
	var missing missingFields
	if st.Limits != nil || st.Samples != nil {
		var err error
		missing, err = probeMissingFields(data)
		if err != nil {
			return nil, fmt.Errorf("water: 读取数据文件失败: %w", err)
		}
	}
	// limitOwnershipErr 记录第一条（分组与版本顺序确定）登记限值归属错误，先不
	// 返回：若同一份文件里还有样品引用了未登记采样点，沿用现有的样品归属错误
	// （保留样品编号与未登记说明）；样品全部通过核对后，限值归属错误仍让整次
	// 打开失败、返回 nil 数据存放。
	var limitOwnershipErr error
	if st.Limits != nil {
		// 不按落盘键分组：键可能是旧版本用单字符拼接生成的，包含 U+0000 的
		// 不同（采样点，项目）组合会共用同一个键。以每条限值自身的两个字段为准，
		// 让曾经被错误合并的组在重新打开时也能各自归位。
		for _, versions := range st.Limits {
			for _, lim := range versions {
				g := limitGroup{pointID: lim.PointID, item: lim.Item}
				s.limits[g] = append(s.limits[g], lim)
			}
		}
		// 按确定性顺序逐组排序并核对：同一生效时刻在同一（采样点，项目）组内
		// 只能有一版，与 SetLimit 登记时的拒绝规则一致——文件里已经存在的重复
		// 版本也必须在打开时整次拒绝，不能读入后让确认时“选到哪一版”取决于
		// 记录排列。分组键序排序只为让重复时报告的组稳定；时间按真实时刻比较
		// （time.Equal 忽略时区写法、保留纳秒精度）：不同时区表示同一瞬间仍算
		// 重复，相差一纳秒则是两版。任一组重复都返回 nil 存放，不合并、不择一、
		// 不跳过该组或其他正常采样点与样品，也不回写原文件。
		groups := make([]limitGroup, 0, len(s.limits))
		for g := range s.limits {
			groups = append(groups, g)
		}
		sort.Slice(groups, func(i, j int) bool {
			if groups[i].pointID != groups[j].pointID {
				return groups[i].pointID < groups[j].pointID
			}
			return groups[i].item < groups[j].item
		})
		for _, g := range groups {
			versions := s.limits[g]
			sort.Slice(versions, func(i, j int) bool { return versions[i].Effective.Before(versions[j].Effective) })
			s.limits[g] = versions
			// 每一版限值都必须先通过采样点归属核对：核对对象是这一版记录自身
			// 保存的 pointId（missing.limitPoint 保留按 JSON 解码后的原样文本），
			// 不是 limits 的落盘集合键。字段缺失、为 null、为空字符串或仅含空白
			// 都算缺少采样点编号；带首尾空白的编号不能整理后匹配；完整编号与本
			// 文件已登记编号逐字比较、不转换大小写，匹配不上即采样点未登记，不
			// 能凭其他限值或样品反过来补登记。同一组各版的解码后编号相同，归属
			// 结论也一致；归属有问题的组先记下第一条错误（顺序确定），并不再
			// 参与生效时间、缺数值与重复时刻核对，确保这类文件统一以
			// ErrCorruptRecord 的归属错误失败，而不是被其他检查抢先报成别的问题。
			groupBad := false
			for _, v := range versions {
				if oerr := limitPointOwnershipError(g, v, missing, s.points); oerr != nil {
					groupBad = true
					if limitOwnershipErr == nil {
						limitOwnershipErr = oerr
					}
				}
			}
			if groupBad {
				continue
			}
			for _, v := range versions {
				// 每一版限值都必须写明从什么时候生效：effective 字段缺失、保存为
				// null 或落盘为 Go 零时间，反序列化后都是零时间，一律视为缺少生效
				// 时间，不能解释成“很早以前就生效”的一版，即使采样点、项目与
				// 上限数值完整也不能放行。核对在缺数值与重复核对之前：两条都缺
				// 生效时间的版本也不能被重复检查抢先报成别的错误。这条核对针对
				// 文件中实际存在的每一版，与当前有没有样品、这一版是否已被样品
				// 采用、同组是否另有完整版本无关：旧版或尚未生效的版本缺少生效
				// 时间同样整次拒绝。不补日期、不借用同项目另一版的日期、不跳过
				// 这一版，也不把拒绝推迟到确认某份样品时。
				if v.Effective.IsZero() {
					return nil, fmt.Errorf("%w: 采样点 %s 的项目 %s 的登记限值缺少生效时间：effective 字段缺失、为 null 或为零时间",
						ErrCorruptRecord, g.pointID, g.item)
				}
			}
			// 每一版限值都必须实际保存了上限数值：value 字段缺失或为 null 时
			// 读出的零值与明确保存的零上限无法区分，只能按字段是否存在核对。
			// 这条核对针对文件中实际存在的每一版，与当前有没有样品、这一版
			// 是否已被样品采用无关：旧版或尚未生效的版本缺少数值同样整次拒绝，
			// 不能只检查最新版本。不补成零、不跳过这一版、不借用同项目另一版
			// 的数值，也不把拒绝推迟到确认某份样品时；明确保存的零不在此列。
			for _, v := range versions {
				if missing.limitValue[g][limitTimeKey(v.Effective)] {
					return nil, fmt.Errorf("%w: 采样点 %s 的项目 %s 于 %s 生效的限值缺少上限数值：value 字段缺失或为 null",
						ErrCorruptRecord, g.pointID, g.item, v.Effective.UTC())
				}
			}
			for i := 1; i < len(versions); i++ {
				if versions[i].Effective.Equal(versions[i-1].Effective) {
					return nil, fmt.Errorf("%w: %s/%s @ %s",
						ErrDuplicateLimitTime, g.pointID, g.item, versions[i].Effective.UTC())
				}
			}
		}
	}
	if st.Samples != nil {
		// 逐份校验后才整体接收：每份样品都必须先通过编号核对（集合编号与
		// 记录自身 id 按 JSON 解码文本逐字一致，不缺编号、不含首尾空白），再
		// 通过采样点归属核对（pointId 完整、不含首尾空白，且逐字命中一个
		// 已登记的采样点，不能凭限值记录或其他样品推测采样点存在），然后才
		// 明确写有三种状态之一（缺失、
		// null、空字符串或其他字符串都是损坏），任何状态的样品都必须保留采样
		// 时间（字段缺失、null 或零时间都是损坏），必须至少保留
		// 一个原测量，
		// 每个原测量项目都必须实际保存了测量值，原测量项目名重复也都是损坏，
		// 带结论的样品还要求逐项判定与原测量完整对应、每条判定都实际保存了
		// 测量值与上限数值、且保存的上限生效时间存在且不晚于采样时间。任一份
		// 样品不通过，整次
		// 打开都失败，不返回数据存放对象，也不静默跳过、择一保留、补测量、
		// 补测量值、补判定、补上限或回写原文件。
		ids := make([]string, 0, len(st.Samples))
		for id := range st.Samples {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err := validateLoadedSample(id, st.Samples[id], missing, s.points); err != nil {
				return nil, err
			}
		}
		s.samples = st.Samples
	}
	// 样品已全部通过（或没有样品）：登记限值的归属错误不能放行，整次打开
	// 失败并返回 nil 数据存放。走到这里说明没有样品归属错误需要沿用——若
	// 样品和限值同时引用未登记采样点，上面的样品核对已经先返回。
	if limitOwnershipErr != nil {
		return nil, limitOwnershipErr
	}
	return s, nil
}

// duplicateSampleID 按文件中的原始 JSON 标记核对所有会被当作样品集合读取的
// 顶层条目，返回第一个（按文件中的出现顺序）出现两次的样品编号；没有重复或
// 文件中没有样品集合时返回空串。
//
// 这一层必须单独核对：json.Unmarshal 反序列化进 map 时，同键的后一条记录会
// 静默覆盖前一条，逐份完整性校验只能看到幸存下来的条目，原本已确认的超标结论
// 可能被另一条同编号的达标结论替换而不被发现。文件顶层写出多个 samples 对象
// 时也一样：反序列化会把它们合并进同一个 map，同一编号分散在不同对象里同样
// 只剩幸存条目，因此编号唯一性按整份文件核对——同一编号无论出现在同一个
// 集合还是不同集合、两次出现之间是否隔着其他样品或顶层字段，都算重复；即使
// 两条记录内容完全相同也按重复编号拒绝，不合并、不择一。字段名按反序列化
// 识别样品集合的同一规则匹配：Samples 等大小写写法同样会被读取为样品集合，
// 核对也必须覆盖，不能借换大小写绕过。编号是否相同按 JSON 解码后的完整文本
// 判断：一处直接写出 "S1"、另一处把 S 写成 Unicode 转义仍是同一个编号。
// 后面再出现空集合或 null 不影响核对：它们只是不再贡献新编号，前面集合里
// 已经出现的重复编号照样报出。样品记录内部的重名键不属于样品集合这一层
// （它们由逐份校验负责），因此逐条跳过记录值，只统计样品集合直属的键。
func duplicateSampleID(data []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", nil
	}
	// seen 跨所有样品集合共享：同一编号分散在多个 samples 对象里同样算重复。
	// 即使文件被改成重复写出多个 samples 属性，也逐个都核对，不依赖反序列化
	// 最终保留了哪一个。
	seen := map[string]struct{}{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, _ := kt.(string)
		// 与 json.Unmarshal 识别 diskState.Samples 的规则一致：字段名按
		// 大小写不敏感匹配，Samples、SAMPLES 等写法都会被当作样品集合读取，
		// 因此都纳入同一次编号核对，不能借换大小写绕过。
		if !strings.EqualFold(key, "samples") {
			if err := skipJSONValue(dec); err != nil {
				return "", err
			}
			continue
		}
		dup, err := firstDuplicateKeyInObject(dec, seen)
		if err != nil || dup != "" {
			return dup, err
		}
	}
	_, err = dec.Token() // 顶层对象结束
	return "", err
}

// firstDuplicateKeyInObject 消费 dec 上紧邻的一个 JSON 对象，把其中直属的键
// 逐个计入 seen（在整份文件的所有样品集合之间共享），按标记出现顺序返回
// 第一个已经在 seen 中出现过的键；紧邻的值不是对象（标量、null 或数组）时
// 按无重复处理，并把该值完整消费掉。
func firstDuplicateKeyInObject(dec *json.Decoder, seen map[string]struct{}) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return "", nil // 标量或 null，单个标记即完整值
	}
	if d != '{' {
		// 数组等非对象值：排空到与起始定界符配平为止。
		return "", drainBalanced(dec)
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, _ := kt.(string)
		// 跳过这条样品记录本身：记录内部各字段即使重名，也不是样品集合层的
		// 编号重复（那是同一份样品记录的内容问题，由逐份校验负责）。
		if err := skipJSONValue(dec); err != nil {
			return "", err
		}
		if _, dup := seen[key]; dup {
			return key, nil
		}
		seen[key] = struct{}{}
	}
	_, err = dec.Token() // 对象结束 '}'
	return "", err
}

// drainBalanced 消费到与已读出的起始 '{'/'[' 配平为止（起始定界符不计入深度）。
func drainBalanced(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// skipJSONValue 消费 dec 上紧邻的一个 JSON 值（标量或任意深度的对象、数组）。
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if _, ok := tok.(json.Delim); !ok {
		return nil // 标量值，单个标记即完整值
	}
	return drainBalanced(dec)
}

// missingFields 汇总对数据文件逐字段核对的结果：JSON 反序列化把数值字段缺失
// 与 null 都落成 float64 零值，与明确保存的零无法区分，因此“有没有保存数值”
// 只能在读取时按字段是否存在单独核对。样品的三个 map 都以落盘样品编号为键，
// 键内是缺少对应字段的项目名集合；登记限值的 map 以限值自身归属的（采样点，
// 项目）组为键，键内是缺少 value 字段的生效时刻（统一按 UTC 文本索引，不同
// 时区写法表示同一时刻归为同一条）。limitPoint 同样以（采样点，项目）组加
// 生效时刻定位到每一版，保留该版记录自身 pointId 字段按 JSON 解码后的文本：
// nil 表示字段缺失或为 null，否则原样保留（含首尾空白），用于核对每版限值的
// 采样点归属——归属只看记录自身保存的编号，不看 limits 的落盘集合键。明确
// 写出数值零（以及负数、正数）的字段不在缺数值的任何一列。
type missingFields struct {
	measValue   map[string]map[string]bool     // 原测量 value 字段缺失或为 null
	resultValue map[string]map[string]bool     // 逐项判定 value 字段缺失或为 null
	resultLimit map[string]map[string]bool     // 逐项判定 limit 字段缺失或为 null
	limitValue  map[limitGroup]map[string]bool // 登记限值 value 字段缺失或为 null
	limitPoint  map[limitGroup]map[string]*string
	sampleID    map[string]*string // 每份样品记录自身的 id 字段：nil 表示字段缺失或为 null，否则为 JSON 解码后的文本
	samplePoint map[string]*string // 每份样品记录自身的 pointId 字段：nil 表示字段缺失或为 null，否则为 JSON 解码后的文本
}

// limitTimeKey 把限值生效时刻规范成可比较的文本键：统一到 UTC 后按纳秒精度
// 格式化，不同时区写法表示同一时刻归为同一条，相差一纳秒仍是两条。
func limitTimeKey(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// probeMissingFields 重新扫描数据文件，找出没有实际保存数值的字段：
// 每份样品中每个原测量项目的 value、每条逐项判定的 value 与 limit，以及每一版
// 登记限值的 value，字段缺失或保存为 null 都计入对应的缺失集合。明确写出数值
// 零（以及负数、正数）的字段不在此列。
//
// 每一版登记限值自身的 pointId 字段也按 JSON 解码后的原样文本保留（字段缺失
// 或为 null 时记 nil，含首尾空白不整理），按（采样点，项目）组与生效时刻定位
// 到具体哪一版，供打开时核对每版限值的采样点归属；归属只看记录自身保存的编号，
// 不看 limits 的落盘集合键。
func probeMissingFields(data []byte) (missingFields, error) {
	var probe struct {
		Limits map[string][]struct {
			PointID   *string   `json:"pointId"`
			Item      string    `json:"item"`
			Value     *float64  `json:"value"`
			Effective time.Time `json:"effective"`
		} `json:"limits"`
		Samples map[string]struct {
			ID           *string `json:"id"`
			PointID      *string `json:"pointId"`
			Measurements []struct {
				Item  string   `json:"item"`
				Value *float64 `json:"value"`
			} `json:"measurements"`
			Results []struct {
				Item  string   `json:"item"`
				Value *float64 `json:"value"`
				Limit *float64 `json:"limit"`
			} `json:"results"`
		} `json:"samples"`
	}
	var missing missingFields
	if err := json.Unmarshal(data, &probe); err != nil {
		return missing, err
	}
	note := func(m *map[string]map[string]bool, key, item string) {
		if *m == nil {
			*m = map[string]map[string]bool{}
		}
		if (*m)[key] == nil {
			(*m)[key] = map[string]bool{}
		}
		(*m)[key][item] = true
	}
	// 登记限值按每条记录自身的采样点与项目归组（与读取时的重新分组一致），
	// 组内按生效时刻定位到具体哪一版缺少数值；不看落盘键。pointId 字段缺失或
	// 为 null 时按空编号归组，随后由归属核对报“缺少采样点编号”，不能让它借
	// 落盘集合键冒充归属。
	for _, versions := range probe.Limits {
		for _, lim := range versions {
			pointID := ""
			if lim.PointID != nil {
				pointID = *lim.PointID
			}
			g := limitGroup{pointID: pointID, item: lim.Item}
			if lim.Value == nil {
				if missing.limitValue == nil {
					missing.limitValue = map[limitGroup]map[string]bool{}
				}
				if missing.limitValue[g] == nil {
					missing.limitValue[g] = map[string]bool{}
				}
				missing.limitValue[g][limitTimeKey(lim.Effective)] = true
			}
			// 保留这一版记录自身 pointId 字段按 JSON 解码后的原样文本（nil 表示
			// 字段缺失或为 null，含首尾空白也不整理），归属核对只认这里，不认
			// limits 的落盘集合键。
			if missing.limitPoint == nil {
				missing.limitPoint = map[limitGroup]map[string]*string{}
			}
			if missing.limitPoint[g] == nil {
				missing.limitPoint[g] = map[string]*string{}
			}
			missing.limitPoint[g][limitTimeKey(lim.Effective)] = lim.PointID
		}
	}
	for key, smp := range probe.Samples {
		// 记录自身的 id 字段按 JSON 解码后的文本原样保留：字段缺失或为 null 时
		// 记为 nil；非字符串写法（数字、布尔、数组、对象）会在前面的整体
		// Unmarshal 阶段报 JSON 类型错误，不会走到这里。
		if missing.sampleID == nil {
			missing.sampleID = map[string]*string{}
		}
		missing.sampleID[key] = smp.ID
		// 记录自身的 pointId 字段同样按 JSON 解码后的文本原样保留：字段缺失或为
		// null 时记为 nil；非字符串写法（数字、布尔、数组、对象）会在前面的
		// 整体 Unmarshal 阶段报 JSON 类型错误，不会走到这里。
		if missing.samplePoint == nil {
			missing.samplePoint = map[string]*string{}
		}
		missing.samplePoint[key] = smp.PointID
		for _, m := range smp.Measurements {
			if m.Value == nil {
				note(&missing.measValue, key, m.Item)
			}
		}
		for _, r := range smp.Results {
			if r.Value == nil {
				note(&missing.resultValue, key, r.Item)
			}
			if r.Limit == nil {
				note(&missing.resultLimit, key, r.Item)
			}
		}
	}
	return missing, nil
}

// validateLoadedSample 校验一份从本地文件读入的样品记录。
//
// 每份样品都必须明确写有三种状态之一（pending、confirmed、voided）：状态决定
// 样品能否产生有效结论，原测量完整、逐项判定齐全或作废原因仍在都不能代替明确的
// 状态。status 字段缺失、保存为 null 或为空字符串，反序列化后都是空状态，一律
// 视为状态缺失；任何其他字符串（含带首尾空白、大小写不同的写法）都是不支持的
// 状态值，不能自动整理后接受，也不能据测量值、判定依据或作废原因猜出它原来是否
// 作废——即使记录保留了完整逐项判定也一样。两种问题都在打开时整次拒绝，错误
// 信息区分状态缺失与不支持的状态值（未知字符串原样带出），不补状态、不改成另
// 一种状态、不产生或替换结论，也不把拒绝推迟到请求确认时。
//
// 状态合法后，每份样品都还必须保留采样时间：sampledAt 字段缺失、保存为 null
// 或落盘为 Go 零时间 0001-01-01T00:00:00Z，反序列化后都是零时间，一律视为缺少
// 采样时间，不能解释成“最早时刻”，也不能补成当前时间或从所用上限的生效时间
// 推测采样日期。这条要求适用于待判定、已确认和已作废的全部样品：作废前是否确认
// 过、是否仍保留逐项判定，都不影响采样时间必须存在；测量项目与数值完整，或者
// 保留了相互一致的达标、超标依据，同样不能代替日期。核对在逐项判定的上限生效
// 时间比较之前进行：已确认记录缺日期时必须明确报告缺少采样时间，不能只说某项
// 上限晚于采样时间。不补日期、不猜日期、不改变样品状态或已有结论，整次打开
// 失败、不回写原文件。
//
// 无论样品处于什么状态，都必须至少保留一个原测量项目：Measurements 为空列表、
// 保存为 null 或整个字段在文件中缺失，都表示样品内容残缺，即使编号、采样点、
// 采样时间齐全也不能接受。待判定样品与待判定后作废（Results 为空、没有历史
// 判定依据）的样品同样适用，不能因为它们不再参与有效判定就放过内容丢失；
// 原测量为空但残留逐项判定的记录也不能反过来用判定内容填补原测量。
//
// 每个原测量项目还必须实际保存了测量值：本地数据中该项目的 value 字段缺失，
// 或字段值为 null，都视为样品内容残缺（由 probeMissingFields 按字段是否存在
// 探出）。即使读出的零值与逐项判定里明确保存的零恰好一致，也不能把缺项当成
// 合法数值；明确保存的数值零仍是合法测量值，正数、负数沿用现有规则。这条要求
// 对任何状态的样品都生效（含待判定、待判定后作废）：缺失的测量值不能让待判定
// 样品随后凭读出的零完成确认，也不能让作废记录借“历史”名义放过丢失的数值。
// 多项目样品只缺一个项目的数值，同样整份拒绝。不填零、不从同项目的逐项判定
// 补值、不重新计算结论。
//
// 原测量项目名也不允许重复：同一个项目名在 Measurements 中只能出现一次，按
// 文件中保存的完整项目名判断，不依赖两条记录是否相邻，也不因数值相同（含两个
// 零）而放行。待判定样品与待判定后作废的样品虽然没有逐项结果，仍要过这一关；
// 它们没有判定记录本身不是损坏，测量项目不重复即正常读入，不会被要求已经有
// 判定依据。
//
// 带结论的样品还要满足：已确认样品，以及已确认后作废、Results 中仍保留历史依据
// 的样品，其原测量项目与逐项判定必须按项目名一一对应；判定中同一项目不能出现
// 两次；每个测量项目必须且只能有一条判定记录，判定中也不能出现原测量没有的
// 项目；对应项目的判定测量值必须与原测量值逐位一致（零是合法测量值，按 map
// 中是否存在该项目判断，不把零当成缺项）。项目按名称对应，与两个列表的排列
// 顺序无关。
//
// 每条逐项判定还必须实际保存了测量值：本地数据中该条判定的 value 字段缺失，
// 或字段值为 null，都视为判定内容残缺（由 probeMissingFields 按字段是否存在
// 探出）。即使读出的零值与原测量明确保存的零一致、或按零与上限比较能得到
// 已保存的达标标记，也不能把缺项当成合法结论；明确保存数值零的判定测量值
// 不在此列。不填零、不从同项目的原测量补值、不重新判定。
//
// 每条逐项判定还必须实际保存了上限数值：本地数据中该条判定的 limit 字段缺失，
// 或字段值为 null，都视为判定依据残缺（由 probeMissingFields 按字段是否存在
// 探出）。即使读出的零值与测量值、超标标记恰好互相对应，也不能把
// 它当成“零等于零”的合法结论；明确保存数值零的上限仍是合法依据，负数和正数
// 上限也保持原有行为，不把零当成缺项。不补成零、不从登记的限值中找值填上、
// 不重新判定。多项目样品只有一项缺少上限，同样整份拒绝。
//
// 每条逐项判定保存的上限生效时间还必须存在且不晚于这份样品的采样时间：确认时
// 只选用采样当时已经生效的上限，保存下来的判定依据也必须遵守同一规则。
// limitEffective 字段缺失、保存为 null 或落盘为 Go 零时间，反序列化后都是零
// 时间，一律视为缺少生效依据，不能当成“一版很早的上限”放行；生效时间晚于采样
// 时间（按真实时刻比较，保留纳秒精度：恰好相等可以接受，晚一纳秒也拒绝；不同
// 时区写法表示同一时刻按相等处理）说明采样时该上限尚未生效，即使测量值、上限
// 数值与超标标记互相对得上也必须拒绝。缺失与晚于采样时间是两种不同的错误信息，
// 后者同时带出两处时间（保留足以说明先后的小数秒）。多项目样品只有一项不符合，
// 同样整份拒绝。不补填日期、不替换上限、不把样品退回待判定，也不重新计算结论。
//
// 对应关系与上限数值都通过后，超标标记还必须与这份样品已保存的判定依据一致：每条判定的
// Exceeded 必须严格等于“该条保存的测量值 > 该条保存的上限”，测量值小于或等于
// 上限（含等于、零或负数的合法数值组合）都应为达标；整份样品的 Exceeded 必须
// 等于“是否有任一项超标”，全部达标则应为假。这里只核对已保存的依据本身，
// 不按当前限值重新选择上限、不重做判定，因此后来补录的限值即使会使结论改变，
// 也不影响读入。单项标记有误点名项目；单项都正确而整份标记相反，说明它与逐项
// 结论不一致。待判定样品与待判定后直接作废、没有历史依据的样品不要求有结论，
// 也不核对超标标记，不要求它们提前保存判定上限。
//
// 上述一切逐份检查之前，先核对样品编号本身：样品集合中这份记录的集合编号
// （落盘 map 的键）必须与记录自身 id 字段按 JSON 解码后的文本逐字一致，并且
// 沿用正常录入后保存的编号形式——录入时编号已去掉首尾空白，因此两处都不能再
// 带首尾空白。id 字段缺失、保存为 null、为空字符串或仅含空白，都算记录缺少
// 编号；集合键或记录 id 任一处带首尾空白，也不能整理空白后放行；两处都完整
// 但文本对不上，属于两处编号不一致。比较不转换大小写、不替换字符：一处直接
// 写 S1、另一处用 Unicode 转义表示同一文本可以接受；S1 与 s1、S1 与 S2 则不
// 一致，中文与编号内部的合法字符仍可使用。缺少编号、编号含首尾空白、两处编号
// 不一致分别给出不同的错误信息，信息都指出集合中的编号，不一致时同时带出两处
// 编号；不补编号、不用集合编号回填、不移动或合并条目、不改变状态或重新判定。
// 整份记录为 JSON null 时没有 id 字段，按缺少编号处理。
//
// 编号核对之后还要核对采样点归属：每份样品的 pointId 必须完整（字段缺失、为
// null、为空字符串或仅含空白都算缺少采样点编号），且不含首尾空白——带首尾
// 空白的编号不能整理后匹配采样点；完整编号按 JSON 解码后的文本与本文件已登记
// 的采样点编号逐字匹配，不转换大小写，匹配不上时明确报告该采样点未登记，不能
// 从限值记录或其他样品推测它存在。这条核对对任何状态的样品都生效：作废记录仍
// 供按点核对，已确认样品的判定依据完整也不能代替采样点已经登记。不补登记采样
// 点、不改写样品归属，整次打开失败、不回写原文件。
//
// key 是落盘 map 中的样品集合编号（JSON 解码后的文本），用于在记录本身残缺
// （如空记录）时仍能指出是哪份样品。points 是本文件中已登记的采样点集合，
// 每份样品保存的采样点编号必须能在其中逐字找到；归属核对只看已登记的采样点，
// 不能从限值记录或其他样品推测某个采样点存在。
func validateLoadedSample(key string, smp *Sample, missing missingFields, points map[string]SamplingPoint) error {
	// 编号核对先于状态、采样时间与测量等一切逐份检查：编号是按点查看、确认与
	// 作废时定位样品的唯一依据。集合编号与记录自身 id 必须逐字一致，且沿用正常
	// 录入保存的形式（不含首尾空白）。id 字段缺失、为 null、为空字符串或仅含
	// 空白都算记录缺少编号；任一处带首尾空白也不能去掉空白后放行；两处文本都
	// 完整但对不上则属于两处编号不一致。比较按 JSON 解码后的文本逐字进行，不转
	// 大小写、不替换字符：一处直接写 S1、另一处用 Unicode 转义表示同一文本可以
	// 接受，S1 与 s1、S1 与 S2 都不一致。整份记录为 JSON null 时没有 id 字段，
	// 同样按缺少编号处理。三种问题都在打开时整次拒绝，错误信息指出集合中的编号，
	// 不一致时同时带出两处编号；不补编号、不用集合编号回填、不移动或合并条目、
	// 不改变状态或重新判定，也不把拒绝推迟到确认或作废时。
	rawID := missing.sampleID[key]
	switch {
	case rawID == nil || strings.TrimSpace(*rawID) == "":
		return fmt.Errorf("%w: 样品集合编号 %s 对应的记录缺少编号：id 字段缺失、为 null、为空字符串或仅含空白，编号不能用集合编号补出",
			ErrCorruptRecord, key)
	case key != strings.TrimSpace(key) || *rawID != strings.TrimSpace(*rawID):
		return fmt.Errorf("%w: 样品集合编号 %q 与记录自身编号 %q 含首尾空白：编号必须沿用正常录入保存的形式，不能整理空白后放行",
			ErrCorruptRecord, key, *rawID)
	case *rawID != key:
		return fmt.Errorf("%w: 样品集合编号 %q 与记录自身的编号 %q 不一致：两处必须是同一文本，不能借集合编号查询、确认或作废另一份样品",
			ErrCorruptRecord, key, *rawID)
	}
	// 通过编号核对后，两处编号逐字相同，后续错误信息统一用它点名样品。
	id := key
	if smp == nil {
		return fmt.Errorf("%w: 样品 %s 的记录为空", ErrCorruptRecord, key)
	}
	// 每份样品都必须明确属于一个已经登记的采样点：正常录入时采样点必须先登记，
	// 打开已有本地数据时也要补上同一核对，否则文件只登记 P1、样品却写着 P9
	// 时，只要其余内容通过检查就会被接受，已确认记录甚至能作为 P9 的最近有效
	// 结果返回。pointId 字段缺失、保存为 null、为空字符串或仅含空白，都按缺少
	// 采样点编号处理；带首尾空白的编号属于损坏，不能去掉空白后替它找一个采样点。
	// 完整编号按 JSON 解码后的文本与已登记编号逐字匹配，不转换大小写：P1 与 p1
	// 是不同编号；一处直接写 P1、另一处用 Unicode 转义表示同一文本则相同。编号
	// 完整但没有对应登记记录时，明确说明该采样点未登记——不能从限值记录或其他
	// 样品推测它存在。这条核对对任何状态的样品都生效：作废记录仍供按点核对，
	// 不能放过丢失的归属；已确认样品的判定依据完整也不能代替采样点已经登记。
	// 不补登记采样点、不改写样品归属，也不把拒绝推迟到确认或按点查询时。
	rawPoint := missing.samplePoint[key]
	switch {
	case rawPoint == nil || strings.TrimSpace(*rawPoint) == "":
		return fmt.Errorf("%w: 样品 %s 缺少采样点编号：pointId 字段缺失、为 null、为空字符串或仅含空白",
			ErrCorruptRecord, id)
	case *rawPoint != strings.TrimSpace(*rawPoint):
		return fmt.Errorf("%w: 样品 %s 的采样点编号 %q 含首尾空白：编号必须完整且沿用正常录入保存的形式，不能整理空白后匹配采样点",
			ErrCorruptRecord, id, *rawPoint)
	}
	if _, ok := points[*rawPoint]; !ok {
		return fmt.Errorf("%w: 样品 %s 的采样点 %q 未登记：样品必须属于本文件中一个已经登记的采样点，不能凭限值记录或其他样品推测采样点存在",
			ErrCorruptRecord, id, *rawPoint)
	}
	// 每份样品都必须明确写有三种状态之一（pending、confirmed、voided）：状态
	// 决定样品能否产生有效结论，原测量完整、逐项判定齐全或作废原因仍在都不能
	// 代替明确的状态。status 字段缺失、为 null 或为空字符串，反序列化后都是
	// 空状态，一律视为状态缺失；任何其他字符串（含带首尾空白、大小写不同的
	// 写法）都是不支持的状态值，不能自动整理后接受，也不能据测量值、判定依据
	// 或作废原因猜出它原来是否作废。两种问题都在打开时整次拒绝，不补状态、
	// 不改成另一种状态、不产生或替换结论，也不把拒绝推迟到请求确认时。
	switch smp.Status {
	case StatusPending, StatusConfirmed, StatusVoided:
	case "":
		return fmt.Errorf("%w: 样品 %s 缺少状态：status 字段缺失、为 null 或为空字符串",
			ErrCorruptRecord, id)
	default:
		return fmt.Errorf("%w: 样品 %s 的状态值 %q 不受支持：只接受 pending、confirmed 或 voided",
			ErrCorruptRecord, id, string(smp.Status))
	}
	// 每份样品都必须保留采样时间：sampledAt 字段缺失、保存为 null 或落盘为
	// Go 零时间 0001-01-01T00:00:00Z，JSON 反序列化后都是零时间，一律视为缺少
	// 采样时间，不能解释成“最早时刻”，也不能补成当前时间或从所用上限的生效
	// 时间推测日期。这条要求对任何状态的样品都生效：待判定样品缺日期就没有按
	// 点台账的排列依据，也无法在确认时选择采样当时已生效的上限；已确认与确认
	// 后作废样品即使测量项目、数值完整，或保留了相互一致的达标、超标依据，也不
	// 能以这些内容代替日期。核对放在逐项判定的上限生效时间比较之前：采样时间本身
	// 缺失时必须明确报告缺少采样时间，而不是笼统地说某项上限“晚于采样时间”。
	// 不补日期、不猜日期、不改变样品状态或已有结论，整次打开失败、不回写原文件。
	if smp.SampledAt.IsZero() {
		return fmt.Errorf("%w: 样品 %s 缺少采样时间：sampledAt 字段缺失、为 null 或为零时间",
			ErrCorruptRecord, id)
	}
	// 至少一个原测量项目是任何状态样品（含待判定、待判定后作废）的读入前提：
	// 空列表、null 或字段缺失都算内容残缺，必须在打开时整次拒绝，不能等请求
	// 确认时才暴露，也不能用残留的逐项判定反填原测量。
	if len(smp.Measurements) == 0 {
		return fmt.Errorf("%w: 样品 %s 缺少原测量：原测量项目列表为空、为 null 或字段缺失",
			ErrCorruptRecord, id)
	}
	// 原测量项目名去重适用于任何状态的样品（含待判定、待判定后作废）：
	// 重复测量是记录本身损坏，不能等确认保存判定依据时才暴露。
	meas := make(map[string]float64, len(smp.Measurements))
	for _, m := range smp.Measurements {
		if _, ok := meas[m.Item]; ok {
			return fmt.Errorf("%w: 样品 %s 的测量项目 %s 重复", ErrCorruptRecord, id, m.Item)
		}
		meas[m.Item] = m.Value
	}
	// 每个原测量项目都必须实际保存了测量值：value 字段缺失或为 null 时读出
	// 的零值与明确保存的零无法区分，只能按字段是否存在核对。这条要求对任何
	// 状态的样品都生效（含待判定、待判定后作废）：缺失的测量值不能让待判定
	// 样品随后凭读出的零完成确认，也不能让作废记录放过丢失的数值。即使读出
	// 的零与逐项判定里明确保存的零一致也不能接受；明确保存的零不在此列。
	// 不填零、不从同项目的逐项判定补值、不重新计算结论。
	for _, m := range smp.Measurements {
		if missing.measValue[key][m.Item] {
			return fmt.Errorf("%w: 样品 %s 的项目 %s 缺少原测量值：value 字段缺失或为 null",
				ErrCorruptRecord, id, m.Item)
		}
	}
	// 待判定样品与待判定后作废（没有历史依据）的样品没有逐项判定，
	// 原测量不重复即为正常记录，不要求它们已经有结论。
	if smp.Status != StatusConfirmed && len(smp.Results) == 0 {
		return nil
	}
	got := make(map[string]float64, len(smp.Results))
	for _, r := range smp.Results {
		if _, ok := got[r.Item]; ok {
			return fmt.Errorf("%w: 样品 %s 的项目 %s 判定记录重复", ErrCorruptRecord, id, r.Item)
		}
		got[r.Item] = r.Value
	}
	// 每条逐项判定都必须实际保存了测量值：value 字段缺失或为 null 时读出的
	// 零值与明确保存的零无法区分，只能按字段是否存在核对。即使读出的零与原
	// 测量明确保存的零一致、或按零与上限比较能得到已保存的达标标记，也不能
	// 把缺项当成合法结论；明确保存的零不在此列。不填零、不从同项目的原测量
	// 补值、不重新判定。
	for _, r := range smp.Results {
		if missing.resultValue[key][r.Item] {
			return fmt.Errorf("%w: 样品 %s 的项目 %s 的逐项判定缺少测量值：value 字段缺失或为 null",
				ErrCorruptRecord, id, r.Item)
		}
	}
	// 每条逐项判定都必须实际保存了上限数值：limit 字段缺失或为 null 时读出
	// 的零值与明确保存的零上限无法区分，只能按字段是否存在核对。即使测量值、
	// 超标标记与读出的零恰好互相对应，也不能把缺项当成“零等于零”的合法结论；
	// 明确保存的零上限不在此列。不补成零、不用登记的限值填上、不重新判定。
	for _, r := range smp.Results {
		if missing.resultLimit[key][r.Item] {
			return fmt.Errorf("%w: 样品 %s 的项目 %s 的判定缺少上限数值：limit 字段缺失或为 null",
				ErrCorruptRecord, id, r.Item)
		}
	}
	// 每条逐项判定保存的上限生效时间必须存在，且不晚于这份样品的采样时间：
	// 确认时只会选用采样当时已经生效的上限，保存的依据也必须遵守同一规则。
	// limitEffective 字段缺失、保存为 null 或落盘为 Go 零时间，JSON 反序列化后
	// 都是零时间，一律视为缺少生效依据——不能当成“一版很早的上限”放行；生效
	// 时间晚于采样时间（按真实时刻比较，保留纳秒精度，恰好相等可以接受，晚一
	// 纳秒也拒绝；不同时区写法表示同一时刻按相等处理）说明采样时该上限尚未
	// 生效，即使测量值、上限数值与超标标记互相对得上也不能接受。不补填日期、
	// 不替换上限、不把样品退回待判定，也不重新计算结论。
	for _, r := range smp.Results {
		if r.LimitEffective.IsZero() {
			return fmt.Errorf("%w: 样品 %s 的项目 %s 的判定缺少上限生效时间：limitEffective 字段缺失、为 null 或为零时间",
				ErrCorruptRecord, id, r.Item)
		}
		if r.LimitEffective.After(smp.SampledAt) {
			return fmt.Errorf("%w: 样品 %s 的项目 %s 的上限生效时间 %s 晚于采样时间 %s：采样时该上限尚未生效",
				ErrCorruptRecord, id, r.Item,
				r.LimitEffective.Format(time.RFC3339Nano), smp.SampledAt.Format(time.RFC3339Nano))
		}
	}
	// 缺项或判定测量值与原测量不一致（按测量列表顺序报告，保持信息稳定）。
	for _, m := range smp.Measurements {
		rv, ok := got[m.Item]
		if !ok {
			return fmt.Errorf("%w: 样品 %s 缺少项目 %s 的判定记录", ErrCorruptRecord, id, m.Item)
		}
		if rv != m.Value {
			return fmt.Errorf("%w: 样品 %s 的项目 %s 判定测量值 %g 与原测量值 %g 不一致",
				ErrCorruptRecord, id, m.Item, rv, m.Value)
		}
	}
	// 判定中出现原测量没有的项目。
	for _, r := range smp.Results {
		if _, ok := meas[r.Item]; !ok {
			return fmt.Errorf("%w: 样品 %s 的判定记录出现了原测量中没有的项目 %s",
				ErrCorruptRecord, id, r.Item)
		}
	}
	// 超标标记必须与这份样品已保存的判定依据一致。单项的超标与否由共用的
	// itemExceeded 判定（与确认新样品时同一规则：严格大于才超标，小于或等于
	// 含等于、零或负数的合法组合都为达标），整份结论由 overallExceeded 汇总
	// （任一单项超标即为真）。这里只按每条记录保存的测量值与上限判断，不重新
	// 选择当前限值；单项标记与依据矛盾时点名该项目，整份标记与逐项结论矛盾时
	// 单独说明。整份样品只允许一种矛盾先报出，但两者都会拒绝。
	itemFlags := make([]bool, 0, len(smp.Results))
	for _, r := range smp.Results {
		want := itemExceeded(r.Value, r.Limit)
		if r.Exceeded != want {
			return fmt.Errorf("%w: 样品 %s 的项目 %s 超标标记与保存的判定依据不一致：测量值 %g %s 上限 %g 应判为%s，却保存为%s",
				ErrCorruptRecord, id, r.Item, r.Value, cmpText(r.Value, r.Limit), r.Limit,
				judgementText(want), judgementText(r.Exceeded))
		}
		itemFlags = append(itemFlags, want)
	}
	anyExceeded := overallExceeded(itemFlags)
	if smp.Exceeded != anyExceeded {
		return fmt.Errorf("%w: 样品 %s 的整份超标标记与逐项判定结论不一致：逐项判定中%s，整份标记却保存为%s",
			ErrCorruptRecord, id,
			overallText(anyExceeded), judgementText(smp.Exceeded))
	}
	return nil
}

// limitPointOwnershipError 核对一版登记限值自身保存的采样点归属。
//
// 核对对象是这版记录自身 pointId 字段按 JSON 解码后的文本（由
// probeMissingFields 保留在 missing.limitPoint 中），不是 limits 的落盘集合
// 键：正常登记入口要求采样点先登记，打开已有本地数据时也要按同一规则核对，
// 否则文件只登记了 P1、却保存着一版归属 P9 的上限时，只要数值与生效时间完整
// 就会被读入，后来登记 P9 并确认样品还会用上这条原本无法写入的限值。
//
// pointId 字段缺失、保存为 null、为空字符串或仅含空白，都算这版限值缺少采样
// 点编号；带首尾空白的编号属于损坏，不能去掉空白后替它匹配一个采样点。完整
// 编号按 JSON 解码后的文本与本文件已登记编号逐字比较，不转换大小写：P1 与
// p1 是不同编号；一处直接写 P1、另一处用 Unicode 转义表示同一文本则相同，
// 中文编号同理，编号内部的 U+0000 也不影响逐字匹配。编号完整但没有对应登记
// 记录时，明确说明该采样点未登记，不能凭其他限值或样品反过来补登记。
//
// 三种问题分别给出不同的错误信息，都以 ErrCorruptRecord 包装、点到对应测量
// 项目与生效时间；含首尾空白与未登记时原样带出该版保存的编号。任一版不符合
// 都由调用方让整次 Open 失败、返回 nil 数据存放，不跳过这一版、不补登记采样
// 点、不改写归属，也不回写原文件；即使尚无样品，或出问题的只是旧版、未来
// 版本也一样。
func limitPointOwnershipError(g limitGroup, v Limit, missing missingFields, points map[string]SamplingPoint) error {
	raw := missing.limitPoint[g][limitTimeKey(v.Effective)]
	switch {
	case raw == nil || strings.TrimSpace(*raw) == "":
		return fmt.Errorf("%w: 项目 %s 于 %s 生效的登记限值缺少采样点编号：pointId 字段缺失、为 null、为空字符串或仅含空白，登记限值必须先属于一个已经登记的采样点，不能用 limits 集合键或其他限值补出归属",
			ErrCorruptRecord, g.item, v.Effective.UTC())
	case *raw != strings.TrimSpace(*raw):
		return fmt.Errorf("%w: 项目 %s 于 %s 生效的登记限值的采样点编号 %q 含首尾空白：编号必须完整且沿用正常登记保存的形式，不能整理空白后匹配采样点",
			ErrCorruptRecord, g.item, v.Effective.UTC(), *raw)
	}
	if _, ok := points[*raw]; !ok {
		return fmt.Errorf("%w: 项目 %s 于 %s 生效的登记限值归属的采样点 %q 未登记：登记限值必须属于本文件中一个已经登记的采样点，不能凭限值或样品反过来补登记",
			ErrCorruptRecord, g.item, v.Effective.UTC(), *raw)
	}
	return nil
}

// itemExceeded 是“测量值是否超过所用上限”的唯一比较规则：测量值严格大于
// 上限才超标，小于或等于（包括恰好等于，以及零与负数的合法数值组合）都达标。
// 确认待判定样品与打开已有数据时核对已保存的判定标记都经过这里，保证相同的
// 测量值和判定依据始终对应相同的单项结论，两处不再各自维护比较逻辑。
func itemExceeded(value, limit float64) bool { return value > limit }

// overallExceeded 是“整份样品是否超标”的唯一汇总规则：多项目样品只要任一
// 单项超标整份就超标，全部单项达标才算整份达标；任何一项达标都不能覆盖另一
// 项的超标。入参按原测量顺序排列，汇总结果与排列无关。确认新样品与打开已有
// 数据时核对整份标记共用这一规则。
func overallExceeded(itemFlags []bool) bool {
	for _, exceeded := range itemFlags {
		if exceeded {
			return true
		}
	}
	return false
}

// cmpText 生成比较词，仅用于损坏记录的错误信息。
func cmpText(value, limit float64) string {
	switch {
	case value > limit:
		return "大于"
	case value < limit:
		return "小于"
	default:
		return "等于"
	}
}

// judgementText 把超标标记翻成判定结论文本，仅用于损坏记录的错误信息。
func judgementText(exceeded bool) string {
	if exceeded {
		return "超标"
	}
	return "达标"
}

// overallText 描述逐项判定汇总后的情况，仅用于损坏记录的错误信息。
func overallText(anyExceeded bool) string {
	if anyExceeded {
		return "存在超标项，整份应判为超标"
	}
	return "全部项目达标，整份应判为达标"
}

// Close 关闭数据存放。已保存的记录已落盘，重新 Open 同一目录即可查看。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return nil
}

func clean(s string) string { return strings.TrimSpace(s) }

// diskLimitKey 生成仅用于落盘 JSON 的分组键。带上两个字段的长度前缀，
// 任何包含 U+0000 的文本组合都不会撞键；读取时并不依赖该键分组。
func diskLimitKey(pointID, item string) string {
	return strconv.Itoa(len(pointID)) + ":" + pointID + strconv.Itoa(len(item)) + ":" + item
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// invalidUTF8 报告文本是否无法作为合法 UTF-8 原样保存。必须在任何处理之前检查
// 提交的原始文本：落盘 JSON 会把非法字节（孤立的 0xFF、0xFE、不完整的多字节
// 序列等）静默替换成 U+FFFD，返回记录与保存内容会因此对不上，两个不同编号也
// 可能变成同一文本。真正的 U+FFFD（“�”）以及中文、U+0000 等都是合法字符，
// 不在拒绝之列；只在去首尾空白之外不做任何字符替换或归一化。
func invalidUTF8(s string) bool { return !utf8.ValidString(s) }

func (s *Store) checkOpenLocked() error {
	if s.closed {
		return ErrClosed
	}
	return nil
}

// persistLocked 把全部数据原子写回数据文件。只在成功变更后调用，
// 被拒绝的操作不会走到这里，因此不会留下新增或变更记录。
func (s *Store) persistLocked() error {
	diskLimits := make(map[string][]Limit, len(s.limits))
	for g, versions := range s.limits {
		diskLimits[diskLimitKey(g.pointID, g.item)] = versions
	}
	data, err := json.MarshalIndent(diskState{
		Points:  s.points,
		Limits:  diskLimits,
		Samples: s.samples,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.file)
}

// RegisterPoint 登记采样点。编号、名称去掉首尾空白后不能为空，
// 保存和比较均使用处理后的文本；重复编号拒绝。
// 编号和名称必须都是合法的 UTF-8 文本：任一字段含孤立的 0xFF、0xFE 字节
// 或不完整的多字节序列等无法原样保存的内容时，整次登记以 ErrInvalidText
// 拒绝，不占用编号、不改动任何已有采样点；即使另一项为空白，或编号已登记而
// 这次名称含非法字节，也报告编码错误。真正的 U+FFFD（“�”）、中文和文本内部
// 的 U+0000 仍是合法字符。
func (s *Store) RegisterPoint(id, name string) (SamplingPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return SamplingPoint{}, err
	}
	// 在去首尾空白和其它任何处理之前检查原始文本：落盘 JSON 会把非法 UTF-8
	// 静默替换成 U+FFFD，登记“成功”后保存的编号或名称就会与填写内容对不上，
	// 两个不同编号（如末尾分别带孤立 0xFF、0xFE）还可能保存成同一编号；
	// 只有整次拒绝才能保证采样点身份在保存前后一致。
	if invalidUTF8(id) || invalidUTF8(name) {
		return SamplingPoint{}, ErrInvalidText
	}
	id, name = clean(id), clean(name)
	if id == "" || name == "" {
		return SamplingPoint{}, ErrEmptyField
	}
	if _, ok := s.points[id]; ok {
		return SamplingPoint{}, fmt.Errorf("%w: %s", ErrDuplicatePoint, id)
	}
	p := SamplingPoint{ID: id, Name: name}
	s.points[id] = p
	if err := s.persistLocked(); err != nil {
		delete(s.points, id)
		return SamplingPoint{}, err
	}
	return p, nil
}

// SetLimit 为已登记采样点的某个测量项目登记一版数值上限及生效时间。
// 采样点编号和项目名必须是合法的 UTF-8 文本：任一字段含孤立的 0xFF、0xFE
// 字节或不完整的多字节序列等无法原样保存的内容时，整次登记以 ErrInvalidText
// 拒绝，不新增版本、不占用生效时间；真正的 U+FFFD（“�”）、中文和文本内部的
// U+0000 仍是合法字符。即使同次请求还存在采样点未登记、数值不是有限数或
// 生效时间缺失的问题，也报告文本编码错误。
// 允许乱序补录；生效时间相同（不同时区表示的同一时刻也算相同）的第二条拒绝。
func (s *Store) SetLimit(pointID, item string, value float64, effective time.Time) (Limit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Limit{}, err
	}
	// 在去首尾空白和其它任何处理之前检查原始文本：落盘 JSON 会把非法 UTF-8
	// 静默替换成 U+FFFD，登记“成功”后保存的项目名就会与填写内容对不上，
	// 限值也不再属于实际填写的采样点和项目；只有整次拒绝才能保证一致。
	if invalidUTF8(pointID) || invalidUTF8(item) {
		return Limit{}, ErrInvalidText
	}
	pointID, item = clean(pointID), clean(item)
	if pointID == "" || item == "" {
		return Limit{}, ErrEmptyField
	}
	if _, ok := s.points[pointID]; !ok {
		return Limit{}, fmt.Errorf("%w: %s", ErrUnknownPoint, pointID)
	}
	if !finite(value) {
		return Limit{}, ErrInvalidValue
	}
	if effective.IsZero() {
		return Limit{}, ErrInvalidTime
	}
	lim := Limit{PointID: pointID, Item: item, Value: value, Effective: effective.UTC()}
	g := limitGroup{pointID: pointID, item: item}
	versions := s.limits[g]
	for _, v := range versions {
		if v.Effective.Equal(lim.Effective) {
			return Limit{}, fmt.Errorf("%w: %s/%s @ %s", ErrDuplicateLimitTime, pointID, item, lim.Effective)
		}
	}
	old := s.limits[g]
	// 复制到新切片再插入排序：append/sort 会原地改动共享底层数组，
	// 一旦保存失败需要回滚，old 必须仍是调用前的完整状态。
	versions = make([]Limit, len(old)+1)
	copy(versions, old)
	versions[len(old)] = lim
	sort.Slice(versions, func(i, j int) bool { return versions[i].Effective.Before(versions[j].Effective) })
	s.limits[g] = versions
	if err := s.persistLocked(); err != nil {
		s.limits[g] = old
		return Limit{}, err
	}
	return lim, nil
}

// SubmitSample 录入一份样品，初始为待判定状态。
// 样品编号、采样点编号和每个项目名必须是合法的 UTF-8 文本：任一字段含孤立的
// 0xFF、0xFE 字节或不完整的多字节序列等无法原样保存的内容时，整份提交以
// ErrInvalidText 拒绝，不留部分记录，也不占用编号；真正的 U+FFFD（“�”）
// 仍是合法字符。再次提交同一编号且内容相同（项目排列顺序不同不算变化）返回原样品；
// 任一内容不同则拒绝整个提交，已确认或已作废的样品同样遵守。
func (s *Store) SubmitSample(id, pointID string, sampledAt time.Time, measurements ...Measurement) (Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Sample{}, err
	}
	// 在去首尾空白和其它任何处理之前检查原始文本：落盘 JSON 会把非法 UTF-8
	// 静默替换成 U+FFFD，只有整份拒绝才能保证返回记录、按点查看与保存数据一致。
	// 即使编号已被已有样品占用、非法项目排在最后，或提交同时存在其它内容问题，
	// 也必须报告编码问题：不能归为未知采样点、项目重复或同编号冲突，更不能走
	// 重复录入成功的分支。
	if invalidUTF8(id) || invalidUTF8(pointID) {
		return Sample{}, ErrInvalidText
	}
	for _, m := range measurements {
		if invalidUTF8(m.Item) {
			return Sample{}, ErrInvalidText
		}
	}
	id, pointID = clean(id), clean(pointID)
	if id == "" || pointID == "" {
		return Sample{}, ErrEmptyField
	}
	if sampledAt.IsZero() {
		return Sample{}, ErrInvalidTime
	}
	if len(measurements) == 0 {
		return Sample{}, ErrNoMeasurements
	}
	ms := make([]Measurement, len(measurements))
	seen := make(map[string]float64, len(measurements))
	for i, m := range measurements {
		item := clean(m.Item)
		if item == "" {
			return Sample{}, ErrEmptyField
		}
		if !finite(m.Value) {
			return Sample{}, ErrInvalidValue
		}
		if _, dup := seen[item]; dup {
			return Sample{}, fmt.Errorf("%w: %s", ErrDuplicateItem, item)
		}
		seen[item] = m.Value
		ms[i] = Measurement{Item: item, Value: m.Value}
	}
	sampledAt = sampledAt.UTC()

	if existing, ok := s.samples[id]; ok {
		if sameSampleContent(existing, pointID, sampledAt, seen) {
			return copySample(existing), nil
		}
		return Sample{}, fmt.Errorf("%w: %s", ErrSampleConflict, id)
	}
	if _, ok := s.points[pointID]; !ok {
		return Sample{}, fmt.Errorf("%w: %s", ErrUnknownPoint, pointID)
	}
	smp := &Sample{
		ID:           id,
		PointID:      pointID,
		SampledAt:    sampledAt,
		Measurements: ms,
		Status:       StatusPending,
	}
	s.samples[id] = smp
	if err := s.persistLocked(); err != nil {
		delete(s.samples, id)
		return Sample{}, err
	}
	return copySample(smp), nil
}

func sameSampleContent(s *Sample, pointID string, sampledAt time.Time, ms map[string]float64) bool {
	if s.PointID != pointID || !s.SampledAt.Equal(sampledAt) || len(s.Measurements) != len(ms) {
		return false
	}
	for _, m := range s.Measurements {
		v, ok := ms[m.Item]
		if !ok || v != m.Value {
			return false
		}
	}
	return true
}

// Confirm 确认一份待判定样品：每个项目取生效时间不晚于采样时间的最近一版上限，
// 测量值大于上限才超标，任一项目超标则整份样品超标。
// 只要有项目找不到适用上限，本次确认拒绝，样品保持待判定。
// 重复确认直接返回已保存结果；之后新增或补录限值不改变已确认结果。
func (s *Store) Confirm(id string) (Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Sample{}, err
	}
	id = clean(id)
	if id == "" {
		return Sample{}, ErrEmptyField
	}
	smp, ok := s.samples[id]
	if !ok {
		return Sample{}, fmt.Errorf("%w: %s", ErrUnknownSample, id)
	}
	switch smp.Status {
	case StatusVoided:
		return Sample{}, fmt.Errorf("%w: %s", ErrVoided, id)
	case StatusConfirmed:
		return copySample(smp), nil
	}

	// 先为每个项目找齐采样当时已生效的最近一版上限：任一项目缺少适用上限都
	// 整次拒绝，不产生部分结果。找齐后与打开已有记录时的核对共用同一套比较
	// 与汇总规则（itemExceeded / overallExceeded），保证相同的测量值和判定
	// 依据在两个场景下始终得到相同结论。
	chosen := make([]Limit, 0, len(smp.Measurements))
	itemFlags := make([]bool, 0, len(smp.Measurements))
	for _, m := range smp.Measurements {
		lim, ok := applicableLimit(s.limits[limitGroup{pointID: smp.PointID, item: m.Item}], smp.SampledAt)
		if !ok {
			return Sample{}, fmt.Errorf("%w: 样品 %s 的项目 %s", ErrMissingLimit, id, m.Item)
		}
		chosen = append(chosen, lim)
		itemFlags = append(itemFlags, itemExceeded(m.Value, lim.Value))
	}
	exceeded := overallExceeded(itemFlags)

	// 逐项结果按原测量顺序返回，顺序与测量列表一致。
	results := make([]ItemResult, 0, len(smp.Measurements))
	for i, m := range smp.Measurements {
		lim := chosen[i]
		results = append(results, ItemResult{
			Item:           m.Item,
			Value:          m.Value,
			Limit:          lim.Value,
			LimitEffective: lim.Effective,
			Exceeded:       itemFlags[i],
		})
	}
	smp.Results = results
	smp.Exceeded = exceeded
	smp.Status = StatusConfirmed
	if err := s.persistLocked(); err != nil {
		smp.Results = nil
		smp.Exceeded = false
		smp.Status = StatusPending
		return Sample{}, err
	}
	return copySample(smp), nil
}

// applicableLimit 在按生效时间升序的版本里取不晚于 at 的最近一版；
// 采样时间恰好等于生效时间时使用该版新值。
func applicableLimit(versions []Limit, at time.Time) (Limit, bool) {
	best := -1
	for i, v := range versions {
		if v.Effective.After(at) {
			break
		}
		best = i
	}
	if best < 0 {
		return Limit{}, false
	}
	return versions[best], true
}

// Void 作废样品。原因去掉首尾空白后不能为空；待判定和已确认的样品都能作废。
// 作废原因必须是合法的 UTF-8 文本：原始原因含孤立的 0xFF、0xFE 字节或不完整
// 的多字节序列等无法原样保存的内容时，本次作废整体以 ErrInvalidText 拒绝，
// 返回空样品，不删除或替换任何字节、不改动样品状态与已有原因；即使样品编号
// 不存在，或样品已作废而这次原因与原原因不同，也报告文本编码错误，而不是
// 样品不存在或原因冲突（数据存放已关闭时仍返回 ErrClosed）。真正的 U+FFFD
// （“�”）、中文、表情和文本内部的 U+0000 仍是合法字符。作废后保留测量、原因
// 和已有结果；重复提交处理后相同的原因返回原记录，不同则拒绝。
func (s *Store) Void(id, reason string) (Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Sample{}, err
	}
	// 在去首尾空白、查找样品和比对原因等任何处理之前检查原始原因：落盘 JSON
	// 会把非法 UTF-8 静默替换成 U+FFFD，作废“成功”后当场返回的原因仍带原始
	// 字节，保存与重开后却变成替换字符，同一记录前后对不上，再次提交原原因
	// 还会被误判成原因冲突；只有整次拒绝才能保证原因在保存前后一致。
	if invalidUTF8(reason) {
		return Sample{}, ErrInvalidText
	}
	id, reason = clean(id), clean(reason)
	if id == "" || reason == "" {
		return Sample{}, ErrEmptyField
	}
	smp, ok := s.samples[id]
	if !ok {
		return Sample{}, fmt.Errorf("%w: %s", ErrUnknownSample, id)
	}
	if smp.Status == StatusVoided {
		if smp.VoidReason == reason {
			return copySample(smp), nil
		}
		return Sample{}, fmt.Errorf("%w: %s", ErrVoidReasonConflict, id)
	}
	oldStatus := smp.Status
	smp.Status = StatusVoided
	smp.VoidReason = reason
	if err := s.persistLocked(); err != nil {
		smp.Status = oldStatus
		smp.VoidReason = ""
		return Sample{}, err
	}
	return copySample(smp), nil
}

// ListByPoint 按采样点查看样品，按采样时间从晚到早排列，同一时刻按样品编号升序。
func (s *Store) ListByPoint(pointID string) ([]Sample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return nil, err
	}
	return s.pointSamplesLocked(clean(pointID), func(*Sample) bool { return true }), nil
}

// LatestResult 返回该采样点最近有效的已确认样品（跳过待判定与已作废）。
// 没有已确认且未作废的样品时 ok 为 false。
func (s *Store) LatestResult(pointID string) (smp Sample, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(); err != nil {
		return Sample{}, false, err
	}
	list := s.pointSamplesLocked(clean(pointID), func(smp *Sample) bool {
		return smp.Status == StatusConfirmed
	})
	if len(list) == 0 {
		return Sample{}, false, nil
	}
	return list[0], true, nil
}

// pointSamplesLocked 收集某采样点符合 keep 的样品，逐份复制后按统一规则排列，
// 供 ListByPoint 与 LatestResult 共用，保证两种查询对同一批样品的先后判断一致。
// 调用方须已持有锁；pointID 须已做首尾空白处理。
func (s *Store) pointSamplesLocked(pointID string, keep func(*Sample) bool) []Sample {
	var list []Sample
	for _, smp := range s.samples {
		if smp.PointID == pointID && keep(smp) {
			list = append(list, copySample(smp))
		}
	}
	sortSamples(list)
	return list
}

func sortSamples(list []Sample) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if !a.SampledAt.Equal(b.SampledAt) {
			return a.SampledAt.After(b.SampledAt)
		}
		return a.ID < b.ID
	})
}

func copySample(s *Sample) Sample {
	c := *s
	c.Measurements = append([]Measurement(nil), s.Measurements...)
	c.Results = append([]ItemResult(nil), s.Results...)
	return c
}
