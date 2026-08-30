package store

// 邀请码（口径层 §2.10，#62 决议 1）：一次性注册凭证。生成/撤销是管理端动作，
// 消费长在注册事务里——码、用户、身份要么一起成，要么一起不成。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// InviteCode 是管理端邀请码列表的一行。
//
// ExpiresAt 用 unix 秒的指针：nil = 不过期。UsedByEmail 直接联查出来——列表要回答
// 的是「这个码谁用掉的」，让前端拿 used_by 再查一遍用户等于把 JOIN 挪到网络上。
type InviteCode struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`
	ExpiresAt   *int64 `json:"expires_at"`
	UsedByEmail string `json:"used_by_email"`
	UsedAt      string `json:"used_at"`
	CreatedAt   string `json:"created_at"`
}

// CreateInviteCodes 批量生成 n 个邀请码，ttl <= 0 即不过期。
//
// 码是 8 字节 crypto/rand 的 hex（16 个字符）：它是能注册进来的唯一门槛，必须
// 猜不出来；再长就抄不动了——邀请码是要发给人手输的。
func CreateInviteCodes(ctx context.Context, db Conn, n int, ttl time.Duration) ([]string, error) {
	if n < 1 || n > 100 {
		return nil, InvalidInput{"一次生成 1~100 个"}
	}
	var expires *int64
	if ttl > 0 {
		v := time.Now().Add(ttl).Unix()
		expires = &v
	}
	codes := make([]string, 0, n)
	for range n {
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		code := hex.EncodeToString(buf)
		if _, err := db.ExecContext(ctx,
			`INSERT INTO invite_codes (code, expires_at) VALUES (?, ?)`, code, expires); err != nil {
			return nil, fmt.Errorf("生成邀请码: %w", err)
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// ListInviteCodes 列全部邀请码，新的在前。已用的行留着——记录使用者正是留它的理由。
func ListInviteCodes(ctx context.Context, db Queryer) ([]InviteCode, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT ic.id, ic.code, ic.expires_at,
		       COALESCE(u.email, ''), COALESCE(ic.used_at, ''), ic.created_at
		FROM invite_codes ic
		LEFT JOIN users u ON u.id = ic.used_by
		ORDER BY ic.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InviteCode{}
	for rows.Next() {
		var c InviteCode
		if err := rows.Scan(&c.ID, &c.Code, &c.ExpiresAt, &c.UsedByEmail, &c.UsedAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeInviteCode 撤销一个**未使用**的邀请码——撤销即删行，这个码从此注册不进来。
//
// 已用的码拒绝撤销：它已经把一个人放进来了，删行只会把「谁用的」这条记录抹掉，
// 而撤销想达成的事（别让人用它注册）早就不可能再发生。
func RevokeInviteCode(ctx context.Context, db Conn, id int64) error {
	res, err := db.ExecContext(ctx,
		`DELETE FROM invite_codes WHERE id = ? AND used_by IS NULL`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	var one int
	err = db.QueryRowContext(ctx,
		`SELECT 1 FROM invite_codes WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return InvalidInput{"这个邀请码已被使用，用过的码不能撤销"}
}

// ConsumeInviteCode 把码用在 userID 身上：一码一人，用后作废。
//
// 判定与写入是同一条 UPDATE——「未使用且未过期」的检查若拆成先查后改，两个并发
// 注册会各自查到「可用」然后各自成功。连接池是 1，这里本没有真并发，但正确性不该
// 依赖那个配置。无效、已用、已过期收成同一句话：注册表单上这三种情况的补救动作
// 相同（找 admin 再要一个码），分开说只是多泄露一点码的状态。
func ConsumeInviteCode(ctx context.Context, db Conn, code string, userID int64) error {
	res, err := db.ExecContext(ctx, `
		UPDATE invite_codes SET used_by = ?, used_at = CURRENT_TIMESTAMP
		WHERE code = ? AND used_by IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)`,
		userID, code, time.Now().Unix())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return InvalidInput{"邀请码无效、已被使用或已过期"}
	}
	return nil
}
