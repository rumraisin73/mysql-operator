package controller

import (
	"context"
	"fmt"

	dbv1 "mysql-operator/api/v1"
)

// 确保基础资源正常
func (r *MysqlClusterReconciler) ensureInfrastructure(ctx context.Context, cluster *dbv1.MysqlCluster) error {

	roles := []string{"master", "slave", "headless"}

	for _, role := range roles {
		if _, err := r.getOrCreateService(ctx, role, cluster); err != nil {
			return fmt.Errorf("4.获取或创建service失败: %w", err)
		}
	}

	configMapName := fmt.Sprintf("%s-configmap", cluster.Name)
	cm, err := r.getOrCreateConfigMap(ctx, configMapName, cluster)
	if err != nil {

		return fmt.Errorf("4.获取或创建configMap失败: %w", err)
	}

	configHash, err := r.computeConfigMapHash(cm)
	if err != nil {
		return fmt.Errorf("4.configMap计算哈希失败: %w", err)
	}

	statefulSetName := fmt.Sprintf("%s-statefulset", cluster.Name)
	if _, err := r.getOrCreateStatefulSet(ctx, statefulSetName, configHash, cluster); err != nil {
		return fmt.Errorf("4.获取或创建statefulSet失败: %w", err)
	}

	return nil
}
