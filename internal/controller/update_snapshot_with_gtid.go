package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

// 并发执行，连接数据库填充快照的gtid和isConnectable

func (r *MysqlClusterReconciler) updateSnapshotWithGTID(ctx context.Context, snapshot *ClusterSnapshot) error {

	var wg sync.WaitGroup

	errChan := make(chan error, len(snapshot.Pods))

	for _, pod := range snapshot.Pods {

		if !pod.IsReady {
			continue
		}

		wg.Add(1)
		go func(p *PodInfo) {
			defer wg.Done()

			// 连接并查询gtid
			gtid, err := r.queryPodGTID(ctx, p, snapshot.RootPassword)
			if err != nil {

				// 标记为不可连
				p.IsConnectable = false

				errChan <- fmt.Errorf("7.节点%s的数据库连接失败: %w", p.Pod.Name, err)
				return
			}

			p.GTID = gtid
			p.IsConnectable = true
		}(pod)
	}

	wg.Wait()
	close(errChan)

	var aggErr error
	for err := range errChan {

		aggErr = errors.Join(aggErr, err)
	}

	return aggErr
}

// 单个节点的连接与查询gtid
func (r *MysqlClusterReconciler) queryPodGTID(ctx context.Context, pod *PodInfo, password string) (string, error) {

	dsn := fmt.Sprintf("root:%s@tcp(%s:3306)/mysql?timeout=1s&readTimeout=1s&parseTime=true&interpolateParams=true", password, pod.Pod.Status.PodIP)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return "", fmt.Errorf("7.1.1dsn格式错误或驱动未加载: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return "", fmt.Errorf("7.2节点%s无法连接: %w", pod.Pod.Name, err)
	}

	var gtid string
	// 查询GTID

	err = db.QueryRowContext(ctx, "SELECT @@global.gtid_executed").Scan(&gtid)
	if err != nil {
		return "", fmt.Errorf("7.3节点%s的gtid获取失败: %w", pod.Pod.Name, err)
	}

	return gtid, nil
}
