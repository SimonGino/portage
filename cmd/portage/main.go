// Command gateway is the single binary: load config, open the database, apply
// the startup gate, then serve.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SimonGino/portage/internal/admin"
	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/declcfg"
	"github.com/SimonGino/portage/internal/server"
	"github.com/SimonGino/portage/internal/store"
)

func main() {
	configPath := flag.String("config", "config.yaml", "启动配置文件路径，缺失时全用默认值")
	// 声明文件路径**默认空**，空即「没挂」（口径层 §2.9 #34）。刻意不给隐式默认值：
	// 自动去找 ./channels.yaml 会让「文件名打错」退化成静默走库配置——那时路径为空是
	// 合法的，Load 里那道「非空却读不到就拒启」的闸根本不触发，人看到的是一切正常、
	// 实际跑的是库里的旧配置。这正是要躲的 litellm 那个坑。
	channelsPath := flag.String("channels", "", "声明文件路径（业务配置）；不给即不挂，配置以 DB 为准")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	// env 覆盖 flag，方向同 PORTAGE_ADMIN_PASSWORD：env 是部署时才知道的，flag 是
	// 命令行里写死的。**必须有 env 这条路**——容器的 ENTRYPOINT 把 -config 写死了，
	// 只给 flag 的话挂一份声明文件就得覆盖整个 entrypoint。
	//
	// 不落进 config.applyEnv：那个函数只认 Config 结构体，而声明文件路径按口径层
	// §2.9 #34 不进 config.yaml——把拒启闸挂在一份「缺失即静默用默认值」的文件上
	// 不成立。这里读一次就够。
	if v := os.Getenv("PORTAGE_CHANNELS"); v != "" {
		*channelsPath = v
	}

	if err := run(*configPath, *channelsPath, log); err != nil {
		log.Error("gateway 启动失败", "err", err)
		os.Exit(1)
	}
}

func run(configPath, channelsPath string, log *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	// 路径为空则 file 为 nil = 没挂声明文件；非空却读不到就是启动即拒，不静默降级。
	file, err := declcfg.Load(channelsPath)
	if err != nil {
		return err
	}
	// 挂了文件即声明文件形态：文件是业务配置唯一事实源，管理端写接口回 409（#48）。
	// 在这里填而不是在 config.Load 里——声明文件的路径本就不进 config.yaml（#34），
	// 只有 main 同时看得见两边。
	cfg.Declarative = file != nil

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// apply 排在 Validate **之前**（口径层 §2.9 #30）。理由不是「空库会被 Validate
	// 拒」——那六项全是「捞出违规行」式检查、空库全过；而是反过来的：apply 若排在
	// 后面，Validate 校的就是一个还没被文件填过的库，文件里的错整个逃出闸外。
	if file != nil {
		if _, err := declcfg.Apply(ctx, db, file, log); err != nil {
			return err
		}
	}

	// 两道闸不合并。挂了文件时 Apply 内部已经在同一个事务里跑过一遍这六项（那样才
	// 能在回滚前把 problems 一起报出来），这里再跑一次是零成本的空转；不挂文件时它
	// 是唯一的那道闸，所以保持无条件调用，别为了省这一次而把它写成条件分支。
	if err := store.Validate(ctx, db); err != nil {
		return err
	}

	// 空 api_keys 表意味着每个转发请求都会 401。按口径层 v0.21 通则这该「启动即报」，
	// 而这里的处置**按形态分岔**（口径层 v0.92）：
	//
	//   - **没挂声明文件**：只警告不拒启。干净库第一次起来必然是空的，而配 key 得先
	//     有个跑着的网关（开 /admin 或对着它建的库灌 SQL），拒启会把这两条路一起堵死。
	//   - **挂了声明文件**：拒启。上面那条立论的两条路同时消失——管理面按密码闸不
	//     注册，灌 SQL 被「文件是唯一事实源」封掉，而 API Key 必须在文件里显式给值。
	//     例外失去依据，回归通则。
	//
	// 拒启那一支不在这里判，落在 declcfg 的 selfCheck 里：那样它才能跟同一份文件里的
	// 其余问题一次报全，而不是抢在前面把它们盖住、把一次重启变两次。
	if n, err := store.CountAPIKeys(ctx, db); err != nil {
		return err
	} else if n == 0 && file == nil {
		log.Warn("api_keys 表是空的，所有转发请求都会回 401；开 /admin 建一把，无 UI 的部署见 scripts/seed-example.sql 的「网关 key」一节")
	}

	// 管理端密码：配置里的明文只用来**初始化**，库里已经有了就一概不动
	// （口径层 §2.7「登录后可改，改后配置项失效」）。同样只警告不拒启。
	//
	// 没设密码不再只是「管理端登不进去」，而是**纯转发形态**（口径层 §2.9 #27）：
	// server.Engine 那边整个 admin.Mount 不调，/admin 与 /admin/api/* 一律 404。
	// 日志措辞跟着改——报「登不进去」会让人以为是密码错了，跑去查会话或 cookie。
	if ok, err := admin.Bootstrap(ctx, db, cfg.AdminPassword); err != nil {
		return err
	} else if !ok {
		log.Warn("未设置管理密码，本进程是纯转发形态：/admin 与 /admin/api/* 整个不注册（404），只提供 /v1 转发面；" +
			"要管理面就填 config.yaml 的 admin_password 或设 PORTAGE_ADMIN_PASSWORD 后重启")
	}

	// 流水保留期（口径层 v0.93，#35）：启动清一次 + 之后每 24h 一次，ctx 取消即退。
	// 常年不重启的实例正是这个缺口的主场景，所以不能只在启动时清。循环放这儿不放
	// store——goroutine、ticker、日志是进程生命周期编排，store 只留纯删除。
	//
	// <= 0 整个不起：0 是「永久保留」不是「每天删光」；负数也必须落在这一侧——
	// 它折出来的 cutoff 在未来，那条 DELETE 会把流水清空，不能给它任何跑起来的机会。
	if days := cfg.CallLogRetentionDays; days > 0 {
		go func() {
			prune := func() {
				n, err := store.DeleteCallLogsBefore(ctx, db, time.Now().AddDate(0, 0, -days))
				if err != nil {
					log.Error("call_logs 保留期清理失败", "err", err)
					return
				}
				if n > 0 {
					log.Info("call_logs 保留期清理", "deleted", n, "retain_days", days)
				}
			}
			prune()
			t := time.NewTicker(24 * time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					prune()
				}
			}
		}()
	}

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: server.New(cfg, db, log).Engine(),
		// 不设 WriteTimeout：它会掐断长 SSE 流。写超时改由 relay 用
		// http.NewResponseController(w).SetWriteDeadline 逐次推进。
		ReadHeaderTimeout: 20 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("gateway 已启动", "listen", cfg.Listen, "db", cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("收到退出信号，等待在途请求结束")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
