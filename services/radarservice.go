package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	radarTableURL         = "https://api.codexradar.com/api/v1/table"
	radarRequestTimeout   = 30 * time.Second
	radarMaxResponseBytes = 8 << 20
)

// 由原站公式直接计算，避免维护近似常量后发生静默的横轴偏差。
var radarCostTimeWeight = math.Log(2.5) / math.Log(1.35)

// RadarService 负责读取公开雷达数据，并在后端完成聚合。
// 原站接口未返回 CORS 许可头，不能让 Wails WebView 直接请求，否则会被浏览器拦截。
type RadarService struct {
	client   *http.Client
	endpoint string
}

// RadarSnapshot 是前端渲染雷达页所需的最小稳定数据契约。
// 不返回任务原始明细，避免把大量无关数据通过 Wails bridge 传给界面层。
type RadarSnapshot struct {
	SourceUpdatedAt string       `json:"source_updated_at"`
	FetchedAt       string       `json:"fetched_at"`
	Points          []RadarPoint `json:"points"`
}

// RadarPoint 表示一个模型与推理强度组合的聚合指标。
type RadarPoint struct {
	Model             string   `json:"model"`
	Effort            string   `json:"effort"`
	IQ                float64  `json:"iq"`
	Passed            int      `json:"passed"`
	ValidTasks        int      `json:"valid_tasks"`
	AveragePriceUSD   *float64 `json:"average_price_usd"`
	PriceSamples      int      `json:"price_samples"`
	AverageMinutes    *float64 `json:"average_minutes"`
	DurationSamples   int      `json:"duration_samples"`
	CombinedCostIndex *float64 `json:"combined_cost_index"`
}

type radarTablePayload struct {
	Combos []radarCombo         `json:"combos"`
	Tasks  []radarTask          `json:"tasks"`
	Cells  map[string]radarCell `json:"cells"`
}

type radarCombo struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type radarTask struct {
	ID string `json:"id"`
}

type radarCell struct {
	RanBy []radarRun `json:"ran_by"`
}

type radarRun struct {
	Passed        *bool    `json:"passed"`
	DurationSec   *float64 `json:"duration_sec"`
	ActualCostUSD *float64 `json:"actual_cost_usd"`
	CostComplete  bool     `json:"cost_complete"`
	GradedAt      string   `json:"graded_at"`
}

type radarAggregation struct {
	point           RadarPoint
	rawCombinedCost *float64
}

// NewRadarService 创建生产环境服务。端点固定，避免任何外部 URL 输入进入桌面端网络边界。
func NewRadarService() *RadarService {
	return newRadarService(radarTableURL, &http.Client{Timeout: radarRequestTimeout})
}

func newRadarService(endpoint string, client *http.Client) *RadarService {
	if client == nil {
		client = &http.Client{Timeout: radarRequestTimeout}
	}
	return &RadarService{
		client:   client,
		endpoint: endpoint,
	}
}

// GetSnapshot 拉取一次原站数据并返回已聚合的页面快照。
func (s *RadarService) GetSnapshot() (*RadarSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), radarRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create radar request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request radar table: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("request radar table: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload radarTablePayload
	decoder := json.NewDecoder(io.LimitReader(resp.Body, radarMaxResponseBytes))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode radar table: %w", err)
	}

	snapshot, err := aggregateRadarTable(payload)
	if err != nil {
		return nil, err
	}
	snapshot.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	return snapshot, nil
}

func aggregateRadarTable(payload radarTablePayload) (*RadarSnapshot, error) {
	if len(payload.Combos) == 0 || len(payload.Tasks) == 0 || len(payload.Cells) == 0 {
		return nil, fmt.Errorf("invalid radar table payload")
	}

	aggregations := make([]radarAggregation, 0, len(payload.Combos))
	var sourceUpdatedAt time.Time

	for _, combo := range payload.Combos {
		model := strings.TrimSpace(combo.Model)
		effort := strings.TrimSpace(combo.Effort)
		if model == "" || effort == "" {
			continue
		}

		passed := 0
		validTasks := 0
		priceSum := 0.0
		priceSamples := 0
		durationMinutesSum := 0.0
		durationSamples := 0

		for _, task := range payload.Tasks {
			if task.ID == "" {
				continue
			}
			cell, ok := payload.Cells[radarCellKey(task.ID, model, effort)]
			if !ok || len(cell.RanBy) == 0 {
				continue
			}

			// 原站按任务列表中的第一条运行记录统计，保持卡片和图表口径一致。
			run := cell.RanBy[0]
			if run.Passed != nil {
				validTasks++
				if *run.Passed {
					passed++
				}
			}
			if run.DurationSec != nil && *run.DurationSec > 0 {
				durationMinutesSum += *run.DurationSec / 60
				durationSamples++
			}
			if run.ActualCostUSD != nil && *run.ActualCostUSD >= 0 {
				// ultra 存在不完整成本样本，混入会把综合成本指标压低，因此只接受完整核算记录。
				if effort != "ultra" || run.CostComplete {
					priceSum += *run.ActualCostUSD
					priceSamples++
				}
			}
			if gradedAt, ok := parseRadarTime(run.GradedAt); ok && gradedAt.After(sourceUpdatedAt) {
				sourceUpdatedAt = gradedAt
			}
		}

		if validTasks == 0 {
			continue
		}

		point := RadarPoint{
			Model:           model,
			Effort:          effort,
			IQ:              float64(passed) / float64(validTasks) * 150,
			Passed:          passed,
			ValidTasks:      validTasks,
			PriceSamples:    priceSamples,
			DurationSamples: durationSamples,
		}
		if priceSamples > 0 {
			averagePrice := priceSum / float64(priceSamples)
			point.AveragePriceUSD = &averagePrice
		}
		if durationSamples > 0 {
			averageMinutes := durationMinutesSum / float64(durationSamples)
			point.AverageMinutes = &averageMinutes
		}

		var rawCombinedCost *float64
		if point.AveragePriceUSD != nil && point.AverageMinutes != nil && *point.AveragePriceUSD > 0 && *point.AverageMinutes > 0 {
			value := *point.AveragePriceUSD * math.Pow(*point.AverageMinutes/10, radarCostTimeWeight) * 100
			rawCombinedCost = &value
		}
		aggregations = append(aggregations, radarAggregation{point: point, rawCombinedCost: rawCombinedCost})
	}

	if len(aggregations) == 0 {
		return nil, fmt.Errorf("radar table contains no valid model points")
	}

	maxRawCost := 0.0
	for _, aggregation := range aggregations {
		if aggregation.rawCombinedCost != nil && *aggregation.rawCombinedCost > maxRawCost {
			maxRawCost = *aggregation.rawCombinedCost
		}
	}

	points := make([]RadarPoint, 0, len(aggregations))
	for _, aggregation := range aggregations {
		point := aggregation.point
		if aggregation.rawCombinedCost != nil && maxRawCost > 0 {
			index := *aggregation.rawCombinedCost / maxRawCost * 100
			point.CombinedCostIndex = &index
		}
		points = append(points, point)
	}

	snapshot := &RadarSnapshot{Points: points}
	if !sourceUpdatedAt.IsZero() {
		snapshot.SourceUpdatedAt = sourceUpdatedAt.UTC().Format(time.RFC3339)
	}
	return snapshot, nil
}

func radarCellKey(taskID, model, effort string) string {
	return taskID + "|" + model + "|" + effort
}

func parseRadarTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
