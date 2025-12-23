package controller

import (
	"context"
	"fmt"
	dbv1 "mysql-operator/api/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// 检查指定的secret是否存在且格式正确，并返回密码供连接数据库
func (r *MysqlClusterReconciler) checkSecret(ctx context.Context, secretName string, cluster *dbv1.MysqlCluster) (string, string, error) {
	existingSecret := &corev1.Secret{}

	err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: secretName}, existingSecret)

	if errors.IsNotFound(err) {
		return "", "", fmt.Errorf("3.必需的secret '%s'不存在。请在Namespace '%s'中手动创建该secret，并包含'root-password和repl-password'键：%w", secretName, cluster.Namespace, err)
	}

	if err != nil {
		return "", "", fmt.Errorf("3.获取secret失败: %w", err)
	}

	// 不需要解码，client已经帮我们干了，但是返回类型是[]byte，要做类型转换
	rootPassword, ok := existingSecret.Data["root-password"]
	if !ok {
		return "", "", fmt.Errorf("3.secret '%s'存在，但缺少必需的键:'root-password'", secretName)
	}
	replPassword, ok := existingSecret.Data["repl-password"]
	if !ok {
		return "", "", fmt.Errorf("3.secret '%s'存在，但缺少必需的键:'repl-password'", secretName)
	}

	return string(rootPassword), string(replPassword), nil
}
