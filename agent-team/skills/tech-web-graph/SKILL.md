---
name: tech-web-graph
description: 为 Web 知识图谱和依赖图选择数据解析、3D 交互或万级节点的 2D 渲染方案，验证关系溯源与大图浏览体验。
---

# Web 图谱开发

## 准备数据

1. 已有导出、结构化 API 或审定的关系库时，用脚本适配。代码仓库的调用、导入和继承图可用 Graphify，读取 `graphify-out/graph.json`，校验 `graph.schema_version`，保留 `EXTRACTED` 与 `INFERRED` 的区别。文档与媒体的语义提取可能调用模型，先确认允许使用的资料与模型。 [Graphify 文档](https://github.com/Graphify-Labs/graphify#readme)
2. 用同一份有版本的节点和关系数据驱动各视图。节点用稳定 ID，边用独立 ID 和有序端点。领域已有谓词、极性、适用条件和证据时，保留关系到各支持命题及版本化出处的对应关系。校验 ID 唯一、边端点和证据索引可解析。访问受限时在服务端按调用者裁剪节点、边和出处，再生成可传到浏览器的布局和索引。
3. 为渲染器创建专用输入。布局坐标、节点索引和高亮状态在渲染层维护，点击结果映射回稳定 ID 后打开业务详情。映射随筛选结果和数据版本一起替换。

## 选择渲染器

| 用户要做的事 | 选型与接入 |
| --- | --- |
| 旋转图谱、聚焦对象、沿关系探索 | 用 `react-force-graph-3d` 的 `ForceGraph3D`。给组件可独立修改的 `{nodes, links}`，限制子图范围与布局迭代。用曲率区分并行边，用箭头表达方向，标签优先显示选中项。 [组件 API](https://github.com/vasturiano/react-force-graph#api-reference) |
| 在万级节点中检索、筛选并读清关系 | 用 Sigma.js 和 Graphology。对有向并行关系用 `MultiDirectedGraph` 和稳定边键，可预计算坐标或在 Worker 运行 ForceAtlas2。Sigma v4 可配置曲线、箭头和按需标签，需要直接点边时启用 `enableEdgeEvents`。全图展示社区和关键节点，放大或聚焦时显示邻域和标签。实时布局可另测 `@sigma/layout-fa2-gpu`，核对 GPU 扩展和运行时更新图结构的行为。 [并行边](https://www.sigmajs.org/how-to/edges/parallel-edges-self-loops/) · [交互事件](https://www.sigmajs.org/reference/events/) · [ForceAtlas2 Worker](https://graphology.github.io/standard-library/layout-forceatlas2.html#webworker) · [GPU 布局](https://github.com/jacomyal/sigma.js/tree/main/packages/layout-fa2-gpu) |
| 在目标设备上对大图实时重排、拖拽和缩放 | 评估 `@cosmos.gl/graph`。它用 WebGL 做渲染和力模拟。以 `Float32Array` 提交二维坐标与边的端点索引，维护索引到业务 ID 的映射。在目标设备验证 WebGL 与 GPU 支持。核心渲染的同向并行边可能重叠，标签用独立层，逐条证据通过聚焦节点的邻边列表访问。 [数据及布局 API](https://github.com/cosmosgl/graph#readme) · [曲线绘制源码](https://github.com/cosmosgl/graph/blob/main/src/modules/Lines/draw-curve-line.vert) |

## 复验

- 用一份小图包含同向并行、反向、否定、有条件的边，以及一条具有多个出处的命题。验证检索、筛选、键盘可达的列表、逐条打开出处和切换资料版本。确认筛选后仍打开选中的边，失效出处和无权查看的关系不会出现在图数据中。
- 用约 1 万节点、与目标业务相近的边数和度数分布比较 2D 渲染器，包含枢纽、并行边及真实标签语言。先用同一份预布局坐标测首次可交互时间、缩放和平移的帧耗时、点选响应，再单独测实时布局。保持视口、像素比、可见标签和交互范围一致，记录浏览器、GPU、库版本、节点和边数及是否启用模拟。用搜索定位、邻域展开与版本化证据点击的实跑结果检验清晰度。
- 先跑页面正确性检查，再在目标浏览器和 GPU 上跑性能验收。报告实测场景、测量口径及未验证项。文档中的规模示例仅用于选型。
