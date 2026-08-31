package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// QuotaState 是配额闸一次判定要的两个数（口径层 §2.10，#65/#75）。
type QuotaState struct {
	// LimitUSD 是用户的月度限额：nil = 不限额（默认），0 = 封停。
	LimitUSD *float64
	// SpentUSD 是本月（UTC 自然月）已落库流水的 SUM(cost)。cost 为 NULL 的行
	// （没有用量可计）天然不计——不预扣的另一面。
	SpentUSD float64
}

// UserQuotaState 取一个用户的配额与本月已用（UTC 自然月，#65）。
//
// 一条语句把限额与 SUM 一起取回：配额闸在热路径上（令牌桶后、Resolve 前），
// 两趟查询就是两次单连接排队。SUM 走 idx_call_logs_user_created 覆盖索引
// （user_id, created_at, cost 三列都在索引里），月首在 Go 侧折成 UTC 串再比，
// 不对 created_at 套函数——套了索引就废了（与 windowStart 同一条纪律）。
//
// 用户不存在时回 ErrNotFound：闸只对有主 key 开，key 指向的用户没了是数据损坏，
// 不该静默当成「不限额」放行。
func UserQuotaState(ctx context.Context, db Queryer, userID int64, now time.Time) (QuotaState, error) {
	monthStart := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	var s QuotaState
	err := db.QueryRowContext(ctx, `
		SELECT u.monthly_quota_usd,
		       COALESCE((SELECT SUM(cost) FROM call_logs
		                 WHERE user_id = u.id AND created_at >= ?), 0)
		FROM users u WHERE u.id = ?`, sqlTime(monthStart), userID).
		Scan(&s.LimitUSD, &s.SpentUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return QuotaState{}, ErrNotFound
	}
	if err != nil {
		return QuotaState{}, err
	}
	return s, nil
}

// SetUserQuota 调一个用户的月度限额（#65：调额立即生效，当月已用不重算——闸每次
// 现算 SUM，这句话在实现上是免费的）。nil = 不限额，0 = 封停。
func SetUserQuota(ctx context.Context, db Conn, id int64, quotaUSD *float64) error {
	if quotaUSD != nil && *quotaUSD < 0 {
		return InvalidInput{"配额不能是负数：留空 = 不限额，0 = 封停"}
	}
	res, err := db.ExecContext(ctx,
		`UPDATE users SET monthly_quota_usd = ? WHERE id = ?`, quotaUSD, id)
	return affectedOne(res, err)
}
