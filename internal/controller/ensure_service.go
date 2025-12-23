package controller

import (
	"context"
	"fmt"
	dbv1 "mysql-operator/api/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *MysqlClusterReconciler) getOrCreateService(ctx context.Context, role string, cluster *dbv1.MysqlCluster) (*corev1.Service, error) {

	existingService := &corev1.Service{}
	serviceName := fmt.Sprintf("%s-svc-%s", cluster.Name, role)

	err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: serviceName}, existingService)

	if err == nil {
		return existingService, nil
	}

	if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("4.1获取%s失败：%w", serviceName, err)
	}

	newService := r.createService(serviceName, role, cluster)

	// 不需要手动设置OwnerReference，controllerutil.SetControllerReference会帮我们处理
	if err := controllerutil.SetControllerReference(cluster, newService, r.Scheme); err != nil {
		return nil, fmt.Errorf("4.1设置%s的OwnerReference时失败：%w", serviceName, err)
	}

	if err := r.Create(ctx, newService); err != nil {
		return nil, fmt.Errorf("4.1创建%s失败：%w", serviceName, err)
	}

	return newService, nil

}

func (r *MysqlClusterReconciler) createService(serviceName, role string, cluster *dbv1.MysqlCluster) *corev1.Service {
	// 为headless单独设置标签和ClusterIP
	var clusterIP string
	var selector = map[string]string{
		"app": cluster.Name,
	}

	if role == "headless" {
		clusterIP = "None"
	}

	if role != "headless" {
		selector["role"] = role
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				"app":  cluster.Name,
				"role": "svc-" + role,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports: []corev1.ServicePort{
				{
					Port: 3306,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 3306,
					},
					Protocol: corev1.ProtocolTCP,
				},
			},
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: clusterIP,
		},
	}
}
