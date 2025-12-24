package controller

import (
	"context"
	"fmt"
	dbv1 "mysql-operator/api/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *MysqlClusterReconciler) getOrCreateStatefulSet(ctx context.Context, statefulSetName, configHash string, cluster *dbv1.MysqlCluster) (*appsv1.StatefulSet, error) {
	logger := log.FromContext(ctx)
	existingSts := &appsv1.StatefulSet{}
	err := r.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: statefulSetName}, existingSts)

	if err == nil {
		// 定义一个标志位，记录是否发生了变化
		needsUpdate := false

		// 检查configMap Hash
		currentHash := existingSts.Spec.Template.Annotations["checksum/config"]
		if currentHash != configHash {
			if existingSts.Spec.Template.Annotations == nil {
				existingSts.Spec.Template.Annotations = make(map[string]string)
			}
			existingSts.Spec.Template.Annotations["checksum/config"] = configHash
			logger.Info("ConfigMap发生变化，更新Hash", "old", currentHash, "new", configHash)
			needsUpdate = true
		}

		currentReplicas := *existingSts.Spec.Replicas
		desiredReplicas := *cluster.Spec.Replicas

		// 只扩不缩
		if desiredReplicas > currentReplicas {

			logger.Info("触发扩容操作", "current", currentReplicas, "target", desiredReplicas)

			// 创建一个新的int32变量取地址
			val := desiredReplicas
			existingSts.Spec.Replicas = &val
			needsUpdate = true

		} else if desiredReplicas < currentReplicas {

			logger.Info("4.3警告：检测到缩容请求，但当前策略禁止自动缩容。忽略该请求。",
				"desired", desiredReplicas,
				"current", currentReplicas)

		}

		if needsUpdate {

			if err := r.Update(ctx, existingSts); err != nil {
				return nil, fmt.Errorf("4.3更新StatefulSet失败: %w", err)
			}

		}

		return existingSts, nil
	}

	if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("4.3获取%s失败:%w", statefulSetName, err)
	}

	newSts := r.createStatefulSet(statefulSetName, configHash, cluster)

	//设置OwnerReference
	if err := controllerutil.SetControllerReference(cluster, newSts, r.Scheme); err != nil {
		return nil, fmt.Errorf("4.3设置%s的OwnerReference时失败:%w", statefulSetName, err)
	}

	if err := r.Create(ctx, newSts); err != nil {
		return nil, fmt.Errorf("4.3创建%s失败:%w", statefulSetName, err)
	}

	return newSts, nil
}

func (r *MysqlClusterReconciler) createStatefulSet(statefulSetName, configHash string, cluster *dbv1.MysqlCluster) *appsv1.StatefulSet {

	replicas := int32(3) // 默认值
	if cluster.Spec.Replicas != nil {
		replicas = *cluster.Spec.Replicas
	}

	// 标签选择器
	labels := map[string]string{
		"app": cluster.Name,
	}

	// 2. 构建 StatefulSet 对象
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSetName,
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			// ServiceName必须匹配无头服务的名字
			ServiceName: fmt.Sprintf("%s-svc-headless", cluster.Name),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			// 使用RollingUpdate策略
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					// 将configMap的Hash注入 Annotation

					Annotations: map[string]string{
						"checksum/config": configHash,
					},
				},
				Spec: corev1.PodSpec{
					// 容器定义
					Containers: []corev1.Container{
						{
							Name:            "mysql",
							Image:           cluster.Spec.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,

							Resources: cluster.Spec.Resources,

							Ports: []corev1.ContainerPort{
								{
									Name:          "mysql",
									ContainerPort: 3306,
								},
							},

							// 环境变量(引用secret)
							Env: []corev1.EnvVar{
								{
									Name: "MYSQL_ROOT_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: cluster.Spec.SecretName,
											Key:                  "root-password", // secret里这个key存密码
										},
									},
								},
								{
									Name:  "MYSQL_ROOT_HOST",
									Value: "%", // 确保root可以远程连接
								},
							},

							// 将configMap里的my.cnf拷贝到配置目录使其生效
							// init.sh生成server-id并启动mysqld
							Command: []string{
								"/bin/bash",
								"-c",
								"cp /mnt/config/my.cnf /etc/mysql/conf.d/99-custom.cnf && /mnt/config/init.sh && exec /usr/local/bin/docker-entrypoint.sh mysqld",
							},

							// 挂载点
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data", // 对应pvc
									MountPath: "/var/lib/mysql",
								},
								{
									Name:      "config", // 对应configMap
									MountPath: "/mnt/config",
								},
							},

							// 存活探针
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"sh",
											"-c",
											"mysqladmin ping -u root -p${MYSQL_ROOT_PASSWORD}",
										},
									},
								},
								InitialDelaySeconds: 30,
								PeriodSeconds:       10,
								TimeoutSeconds:      5,
							},

							// 就绪探针

							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"sh",
											"-c",
											"mysqladmin ping -u root -p${MYSQL_ROOT_PASSWORD}",
										},
									},
								},
								PeriodSeconds:    2, // 缩短到2秒甚至1秒
								FailureThreshold: 2, // 连续2次失败即剔除
								SuccessThreshold: 1,
								TimeoutSeconds:   1,
							},
						},
					},
					// 定义configMap volume
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: fmt.Sprintf("%s-configmap", cluster.Name),
									},
									DefaultMode: ptr.To(int32(0755)), // 赋予脚本执行权限，注意转成指针
								},
							},
						},
					},
				},
			},
			// 自动创建PVC
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: cluster.Spec.Storage.Size,
							},
						},
						StorageClassName: cluster.Spec.Storage.StorageClassName,
					},
				},
			},
		},
	}

}
