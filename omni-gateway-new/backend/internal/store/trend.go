package store

import (
	"context"

	"github.com/zjvtit/omni-gateway/internal/model"
)

func (s *Store) statsTrend(ctx context.Context, stats *model.StatsSummary, conditions string, args []any) error {
	rows, err := s.pool.Query(ctx, `SELECT date_trunc('hour',created_at),count(*),count(*) FILTER(WHERE status='success'),COALESCE(sum(cost),0),COALESCE(sum(credits),0) FROM requests WHERE `+conditions+` GROUP BY 1 ORDER BY 1 DESC LIMIT 744`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p model.TrendPoint
		if err := rows.Scan(&p.Period, &p.Requests, &p.Success, &p.Cost, &p.Credits); err != nil {
			rows.Close()
			return err
		}
		stats.Trend = append(stats.Trend, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for i, j := 0, len(stats.Trend)-1; i < j; i, j = i+1, j-1 {
		stats.Trend[i], stats.Trend[j] = stats.Trend[j], stats.Trend[i]
	}
	rows, err = s.pool.Query(ctx, `SELECT error_type,count(*) FROM requests WHERE `+conditions+` AND status='error' GROUP BY error_type`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	stats.ErrorTypes = map[string]int64{}
	for rows.Next() {
		var kind string
		var count int64
		if err := rows.Scan(&kind, &count); err != nil {
			return err
		}
		stats.ErrorTypes[kind] = count
	}
	return rows.Err()
}
