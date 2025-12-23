package controller

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	dbv1 "mysql-operator/api/v1"
)

// 数据库内部设置修正
func (r *MysqlClusterReconciler) reconcileDatabaseSettings(ctx context.Context, snapshot *ClusterSnapshot, cluster *dbv1.MysqlCluster) error {

	var targetMasterNode *PodInfo
	for _, node := range snapshot.Pods {
		if node.Role == "master" {
			targetMasterNode = node
			break
		}
	}

	if targetMasterNode == nil {
		return fmt.Errorf("9.快照中未找到role=master的节点")
	}

	// master的无头服务

	masterHost := fmt.Sprintf("%s.%s-svc-headless.%s", targetMasterNode.Pod.Name, cluster.Name, cluster.Namespace)

	var wg sync.WaitGroup

	errChan := make(chan error, len(snapshot.Pods))

	// 并发配置所有节点
	for _, pod := range snapshot.Pods {

		if !pod.IsConnectable {
			continue
		}

		wg.Add(1)
		go func(p *PodInfo) {
			defer wg.Done()

			dsn := fmt.Sprintf("root:%s@tcp(%s:3306)/mysql?timeout=1s&readTimeout=3s&parseTime=true&interpolateParams=true", snapshot.RootPassword, p.Pod.Status.PodIP)
			db, err := sql.Open("mysql", dsn)
			if err != nil {
				errChan <- fmt.Errorf("9.dsn格式错误或驱动未加载: %w", err)
				return
			}
			defer db.Close()

			if err := db.PingContext(ctx); err != nil {
				errChan <- fmt.Errorf("9.节点%s无法连接: %w", p.Pod.Name, err)
				return
			}

			// 根据期望的角色执行配置
			if p.Role == "master" {
				err = r.configureMaster(ctx, db, p.Pod.Name)
			}
			if p.Role == "slave" {
				// slave节点需要知道master的地址和复制账号密码
				err = r.configureSlave(ctx, db, p.Pod.Name, masterHost, snapshot.ReplPassword)
			}

			if err != nil {
				errChan <- fmt.Errorf("9.配置%s数据库内部参数失败: %w", p.Pod.Name, err)
			}

		}(pod)
	}

	wg.Wait()
	close(errChan)

	if len(errChan) > 0 {
		// 返回第一个错误即可，触发Reconcile重试
		return <-errChan
	}

	return nil
}

// 配置主库
func (r *MysqlClusterReconciler) configureMaster(ctx context.Context, db *sql.DB, podName string) error {
	// 设置为可读写
	if _, err := db.ExecContext(ctx, "SET GLOBAL read_only=0"); err != nil {
		return fmt.Errorf("9.1主库节点%s配置可读写失败: %w", podName, err)
	}

	// 如果该节点之前是slave，现在变成了master，需要停止它之前的同步任务
	if _, err := db.ExecContext(ctx, "STOP SLAVE"); err != nil {
		// 忽略错误，可能本来就没启动
	}
	// 清除同步配置，防止它连接旧的主库
	if _, err := db.ExecContext(ctx, "RESET SLAVE ALL"); err != nil {
		return fmt.Errorf("9.1主库节点%s清除同步配置失败: %w", podName, err)
	}

	return nil
}

// 配置从库
func (r *MysqlClusterReconciler) configureSlave(ctx context.Context, db *sql.DB, podName, masterHost, replPwd string) error {

	// 设置为只读
	if _, err := db.ExecContext(ctx, "SET GLOBAL read_only=1"); err != nil {
		return fmt.Errorf("9.2从库节点%s配置只读失败: %w", podName, err)
	}

	// 幂等性检查：检查当前是否已经正常同步且Master地址正确
	isConfigured, err := r.isReplicatingCorrectly(ctx, db, masterHost)
	if err != nil {
		return fmt.Errorf("9.2从库节点%s检查数据库同步状态失败: %w", podName, err)
	}
	if isConfigured {
		return nil
	}

	// 停止同步
	if _, err := db.ExecContext(ctx, "STOP SLAVE"); err != nil {
		return fmt.Errorf("9.2从库节点%s停止同步失败: %w", podName, err)
	}

	// 这部分逻辑取消掉，只要在reconcileUserForPod里开启SET SESSION sql_log_bin = 0
	//var executedGtidSet string
	//err = db.QueryRowContext(ctx, "SELECT @@global.gtid_executed").Scan(&executedGtidSet)
	//if err != nil {
	//	return fmt.Errorf("从库节点%s获取GTID执行集失败: %w", podName, err)
	//}

	// 只有在gtid集合为空（说明是全新节点）时才执行 RESET MASTER
	//if executedGtidSet == "" {

	//	if _, err := db.ExecContext(ctx, "RESET MASTER"); err != nil {
	//		return fmt.Errorf("从库节点%s重置gtid失败: %w", podName, err)
	//	}
	//}

	// 清除旧的同步连接参数
	if _, err := db.ExecContext(ctx, "RESET SLAVE ALL"); err != nil {
		return fmt.Errorf("9.2从库节点%s清除同步参数失败: %w", podName, err)
	}

	// 配置同步源

	changeMasterSQL := fmt.Sprintf(`
		CHANGE MASTER TO 
		MASTER_HOST='%s', 
		MASTER_USER='repl', 
		MASTER_PASSWORD='%s', 
		MASTER_PORT=3306, 
		MASTER_CONNECT_RETRY=10, 
		MASTER_AUTO_POSITION=1;
	`, masterHost, replPwd)

	if _, err := db.ExecContext(ctx, changeMasterSQL); err != nil {
		return fmt.Errorf("9.2从库节点%s配置同步源失败: %w", podName, err)
	}

	// 启动同步
	if _, err := db.ExecContext(ctx, "START SLAVE"); err != nil {
		return fmt.Errorf("9.2从库节点%s启动同步失败: %w", podName, err)
	}

	return nil
}

// 辅助函数：检查slave状态
func (r *MysqlClusterReconciler) isReplicatingCorrectly(ctx context.Context, db *sql.DB, targetMasterHost string) (bool, error) {

	rows, err := db.QueryContext(ctx, "SHOW SLAVE STATUS")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	if !rows.Next() {
		// 如果为空，说明还没有配置过slave
		return false, nil
	}

	// 获取列名，以便扫描，因为不同列的顺序可能会变
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}

	// 创建一个 map 来存储列值
	values := make([]interface{}, len(columns))
	scanArgs := make([]interface{}, len(columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	if err := rows.Scan(scanArgs...); err != nil {
		return false, err
	}

	// 将结果解析为 Map 方便查找
	statusMap := make(map[string]string)
	for i, colName := range columns {
		val := values[i]
		if b, ok := val.([]byte); ok {
			statusMap[colName] = string(b)
		} else {
			statusMap[colName] = ""
		}
	}

	// 检查逻辑：
	// I/O线程必须是Yes
	// SQL线程必须是Yes
	// Master_Host必须匹配目标master
	slaveIORunning := statusMap["Slave_IO_Running"]
	slaveSQLRunning := statusMap["Slave_SQL_Running"]
	currentMasterHost := statusMap["Master_Host"]

	if strings.EqualFold(slaveIORunning, "Yes") &&
		strings.EqualFold(slaveSQLRunning, "Yes") &&
		currentMasterHost == targetMasterHost {
		return true, nil
	}

	return false, nil
}
