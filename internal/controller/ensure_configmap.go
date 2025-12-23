package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	dbv1 "mysql-operator/api/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *MysqlClusterReconciler) getOrCreateConfigMap(ctx context.Context, configMapName string, cluster *dbv1.MysqlCluster) (*corev1.ConfigMap, error) {
	existingConfigMap := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: configMapName}, existingConfigMap)
	if err == nil {
		return existingConfigMap, nil

	}

	if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("4.2获取%s失败：%w", configMapName, err)
	}

	newConfigMap := r.createConfigMap(configMapName, cluster)

	if err := controllerutil.SetControllerReference(cluster, newConfigMap, r.Scheme); err != nil {
		return nil, fmt.Errorf("4.2设置%s的OwnerReference时失败：%w", configMapName, err)
	}

	if err := r.Create(ctx, newConfigMap); err != nil {
		return nil, fmt.Errorf("4.2创建%s失败：%w", configMapName, err)
	}

	return newConfigMap, nil
}

func (r *MysqlClusterReconciler) createConfigMap(configMapName string, cluster *dbv1.MysqlCluster) *corev1.ConfigMap {
	myCnf := fmt.Sprintf(`[mysqld]

binlog_format=row
log-bin=mysql-bin
gtid-mode=on
enforce-gtid-consistency=true
log-slave-updates=1
relay_log_purge=0
# other configurations`)

	initScript := `#!/bin/bash
set -e
ORDINAL=${HOSTNAME##*-}
if [[ ! $ORDINAL =~ ^[0-9]+$ ]]; then
  echo "序号提取错误"
  exit 1
fi
SERVER_ID=$((100 + $ORDINAL))
echo "[mysqld]" > /etc/mysql/conf.d/server-id.cnf
echo "server-id=$SERVER_ID" >> /etc/mysql/conf.d/server-id.cnf
`

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: cluster.Namespace,
		},

		Data: map[string]string{
			"my.cnf":  myCnf,
			"init.sh": initScript,
		},
	}
}

func (r *MysqlClusterReconciler) computeConfigMapHash(cm *corev1.ConfigMap) (string, error) {
	// 序列化为json，保证哈希的唯一性
	dataBytes, err := json.Marshal(cm.Data)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(dataBytes)
	return hex.EncodeToString(hash[:]), nil
}
