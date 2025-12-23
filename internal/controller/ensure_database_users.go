package controller

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	_ "github.com/go-sql-driver/mysql"
)

const (
	// 同步账号用户名
	ReplUser = "repl"
)

// 并发执行，确保所有ready的节点都拥有正确的root和repl账号
func (r *MysqlClusterReconciler) ensureDatabaseUsers(ctx context.Context, snapshot *ClusterSnapshot) error {

	var wg sync.WaitGroup
	var errChan = make(chan error, len(snapshot.Pods))

	// 遍历快照中的所有节点
	for _, pod := range snapshot.Pods {

		if !pod.IsReady {
			continue
		}

		wg.Add(1)
		go func(p *PodInfo) {
			defer wg.Done()

			// 执行单个节点的初始化逻辑
			if err := r.reconcileUserForPod(ctx, p, snapshot.RootPassword, snapshot.ReplPassword); err != nil {

				errChan <- fmt.Errorf("6.节点%s的数据库初始化失败: %w", p.Pod.Name, err)
			}
		}(pod)
	}

	wg.Wait()
	close(errChan)

	// 如果有任何一个节点失败，就返回错误
	if len(errChan) > 0 {
		return <-errChan
	}

	return nil
}

// 单个节点的初始化
func (r *MysqlClusterReconciler) reconcileUserForPod(ctx context.Context, pod *PodInfo, rootPwd, replPwd string) error {

	// interpolateParams=true表示在本地预编译sql语句
	dsn := fmt.Sprintf("root:%s@tcp(%s:3306)/mysql?timeout=1s&readTimeout=3s&parseTime=true&interpolateParams=true", rootPwd, pod.Pod.Status.PodIP)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("6.1dsn格式错误或驱动未加载: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("6.2节点%s无法连接: %w", pod.Pod.Name, err)
	}

	// 关于sql语句，密码和where，value，set后的值使用?占位符
	// 其他的标识符用fmt.Sprintf拼接

	// 保险操作：关闭二进制日志，避免污染gtid
	if _, err := db.ExecContext(ctx, "SET SESSION sql_log_bin = 0"); err != nil {
		return fmt.Errorf("6.3设置sql_log_bin=0失败: %w", err)
	}

	// CREATE USER IF NOT EXISTS 不存在就会创建，存在也不会报错
	// %需要写成%%来转义
	createUserQuery := fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY ?", ReplUser)
	if _, err := db.ExecContext(ctx, createUserQuery, replPwd); err != nil {
		return fmt.Errorf("6.4创建/检查repl用户失败: %w", err)
	}

	// 无论是新建的还是旧的，都强制刷新一次密码
	alterUserQuery := fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY ?", ReplUser)
	if _, err := db.ExecContext(ctx, alterUserQuery, replPwd); err != nil {
		return fmt.Errorf("6.5更新repl用户密码失败: %w", err)
	}

	// 授权也是幂等的，多执行几次没关系
	grantQuery := fmt.Sprintf("GRANT REPLICATION SLAVE ON *.* TO '%s'@'%%'", ReplUser)
	if _, err := db.ExecContext(ctx, grantQuery); err != nil {
		return fmt.Errorf("6.6授权repl用户失败: %w", err)
	}

	// 同样，ALTER USER 是幂等的，放心执行
	rootQuery := "ALTER USER 'root'@'%' IDENTIFIED BY ?"

	if _, err := db.ExecContext(ctx, rootQuery, rootPwd); err != nil {
		return fmt.Errorf("6.7更新root用户密码失败: %w", err)
	}

	if _, err := db.ExecContext(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("6.8刷新权限失败: %w", err)
	}

	return nil
}
