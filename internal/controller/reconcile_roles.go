package controller

import (
	"context"
	"fmt"

	"errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// 专门定义一种错误类型，用来避免拉取镜像导致的指数级退避
var ErrHA = errors.New("集群可用节点数量不足")

// 返回值bool表示是否执行了patch操作
func (r *MysqlClusterReconciler) reconcileRoles(ctx context.Context, snapshot *ClusterSnapshot) (bool, error) {

	logger := log.FromContext(ctx)

	var currentMasters []*PodInfo
	var candidates []*PodInfo

	for _, node := range snapshot.Pods {
		if node.Role == "master" {
			currentMasters = append(currentMasters, node)
		}
		// 参选资格：ready且connectable
		if node.IsReady && node.IsConnectable {
			candidates = append(candidates, node)
		}
	}

	if len(candidates) < 2 {

		return false, ErrHA
	}

	// 选主决策
	var targetMaster *PodInfo
	needElection := false

	switch len(currentMasters) {

	// 情况1: 无主，必须重选
	case 0:
		logger.Info("8.无主库，启动选举流程")
		needElection = true

	// 情况2: 单主，检查现任健康状况
	case 1:
		existingMaster := currentMasters[0]

		// 只要现任主库还活着(IsReady && IsConnectable)，就保持它
		// 即使别的节点gtid追上来了也不换，避免因网络问题导致主库反复横跳
		if existingMaster.IsReady && existingMaster.IsConnectable {
			targetMaster = existingMaster
		} else {
			logger.Info("8.当前主库不健康，启动选举流程", "当前主库名字", existingMaster.Pod.Name)
			needElection = true
		}

	// 情况3: 脑裂，必须重选
	default:
		logger.Info("8.脑裂发生，启动选举流程", "当前主库数量", len(currentMasters))
		needElection = true
	}

	// 执行选举算法
	if needElection {

		// 候选人candidates已经是按podName排序的
		// 逻辑：默认第一个是最佳，后面只有gtid严格大于它才能篡位。
		best := candidates[0]
		for i := 1; i < len(candidates); i++ {
			challenger := candidates[i]

			lenChallenger := len(challenger.GTID)
			lenBest := len(best.GTID)

			// 先比较gtid长度
			if lenChallenger > lenBest {
				best = challenger
				continue
			}

			// 长度相等时，比较字典序
			if lenChallenger == lenBest {
				if challenger.GTID > best.GTID {
					best = challenger
				}
			}

		}
		targetMaster = best
		logger.Info("8.已选出新主", "pod名字", targetMaster.Pod.Name, "gtid", targetMaster.GTID)
	}

	// 打标签
	patched := false

	for _, pod := range snapshot.Pods {
		// 目标角色
		desiredRole := "slave"
		if pod.Pod.Name == targetMaster.Pod.Name {
			desiredRole = "master"
		}

		// 只有不一致时才请求api-server打标签
		if pod.Role != desiredRole {
			// pod对象是指针类型，里面有大量slice和map，必须深拷贝来做前后状态的对比，避免一个改了另一个跟着变
			patch := client.MergeFrom(pod.Pod.DeepCopy())
			pod.Pod.Labels["role"] = desiredRole

			// pod.Pod是希望改成的样子，patch是旧数据的快照，Patch方法通过对比，生成差异补丁，发给api-server
			if err := r.Patch(ctx, pod.Pod, patch); err != nil {
				return false, fmt.Errorf("8.给节点%s打标签失败: %w", pod.Pod.Name, err)
			}
			logger.Info("8.已打标签", "pod名字", pod.Pod.Name, "from", pod.Role, "to", desiredRole)
			patched = true
		}
	}

	return patched, nil
}
