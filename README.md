## mysql-operator

**mysql-operator** 是云原生的大作业，用于在Kubernetes集群中自动化管理mysql实例的生命周期和主从切换。

当前版本：**v0.0.2**

### 版本更新

- v0.0.2 增加了对扩容的支持，修正了一些bug

### 功能特性

- 自动创建mysql集群并初始化，做好主从关系
- 使用statefulset管理mysql实例的副本数，自动重启
- 支持扩容操作，缩容操作自动忽略
- 选举算法使用gtid加固定顺序
- 使用最小权限repl账户同步数据
- 支持修改configmap后自动重启pod
- 优化了kubectl get显示体验

### 快速开始

**前置条件：**
- Kubernetes 集群（测试环境为1.28.15）
- Go v1.22+
- Docker 17.03+
- kubectl
- make

**克隆项目**

```bash
git clone https://github.com/rumraisin73/mysql-operator.git
cd mysql-operator
```

**初始化依赖**

```bash
go mod tidy
go mod download
```

**安装 CRD**

```bash
make install
```

**本地运行**

```bash
make run
```

**部署到集群**

```bash
make docker-build IMG=mysql-operator:v0.0.2
# 自行推送至远程仓库
make deploy IMG=<仓库地址>/mysql-operator:v0.0.2
```

### 示例

**secret（必须）**

```bash
kubectl create secret generic test-secret \
  -n default \
  --from-literal=root-password=root@111 \
  --from-literal=repl-password=repl@111
```

**自定义资源**

```yaml
apiVersion: apps.rumraisin.me/v1  
kind: MysqlCluster
metadata:
  name: test-cluster
  namespace: default
spec:
  # 镜像必须指定
  image: mysql:5.7
  # 副本数可选，最少为2，默认为3
  #replicas: 3
  # storage必须指定
  storage:
    # 1Gi用于测试
    size: 1Gi
    # 可选：不写使用默认storageClass
    storageClassName: standard
  # 资源限制必须指定
  resources:
    requests:
      cpu: 500m
      memory: 512Mi
    limits:
      cpu: 500m
      memory: 512Mi
  # secret必须指定，且有相应的key
  secretName:
    name: test-secret
```

```bash
kubectl apply -f config/samples/test-cluster.yaml
```

**写入测试脚本**

```bash
go run test/test_write.go
```

### 下一步计划

- 增加conditions显示
- 增加对存储扩容的支持
