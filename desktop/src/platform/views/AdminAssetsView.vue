<template>
  <!-- 本地市场（导航名，原「资产审核」）：左 = 全部资产（按状态分块，待审优先），右 = 详情与裁决。
       平台侧只治理不搬运 —— 快照由租户侧上架产生，安装也在租户侧，这里只改状态与可见范围。
       状态机是单向的（draft → pending → listed | rejected，listed → delisted）：
       越级一律后端拒绝，所以按钮按当前状态置灰，而不是点下去再报错。
       删除不在这条状态机上 —— listed / delisted 都能直接删，不要求先下架。 -->
  <div class="section">
    <!-- 顶部一行：左 = 二级切换（全部资产 / 上架策略），右 = 资产列表的筛选与操作。
         筛选只在「全部资产」tab 下有意义，切到上架策略时整组收起 -->
    <div class="sub-bar">
      <div class="sub-tabs">
        <!-- tab 叫「全部资产」而不是「审核队列」：这里管五种状态，待审只是其中一块。
             角标仍是待审数（最该被处理的那些），所以给它一句 title 说明，
             否则「全部资产」上挂个数字会让人以为是总条数 -->
        <button class="sub-tab" :class="{ 'is-active': tab === 'queue' }" @click="switchTab('queue')">
          全部资产
          <span
            v-if="pendingCount"
            class="sub-tab-badge"
            :title="`其中待审 ${pendingCount} 条`"
          >{{ pendingCount }}</span>
        </button>
        <button class="sub-tab" :class="{ 'is-active': tab === 'policy' }" @click="switchTab('policy')">
          上架策略
        </button>
        <!-- 对外接入：谁能访问我的市场。放在本地市场而不是远端市场 ——
             它与「上架策略」是同一件事的两半（上架要不要审 / 上架后谁能读） -->
        <button class="sub-tab" :class="{ 'is-active': tab === 'access' }" @click="switchTab('access')">
          对外接入
        </button>
      </div>

      <div v-show="tab === 'queue'" class="sub-bar-right">
        <!-- 类型用「本地市场」那套药丸 chip：比下拉少一次点击，扫一眼就知道切到哪类 -->
        <button
          v-for="k in KIND_FILTERS"
          :key="'k-' + k.value"
          class="filter-chip"
          :class="{ active: filters.kind === k.value }"
          @click="pickKind(k.value)"
        >{{ k.label }}</button>
        <!-- 状态不做筛选：队列已按状态分块（组头带数量、可收起），
             再看某一档只要把别的块收起来，多一个下拉是同一件事的第二种说法 -->
        <!-- 来源：接入多个远端市场后，队列里必须能分开「本机上架」与各站点导入的行。
             常驻显示，不按 origins 数量隐藏 —— 单机时它就一项「本机上架」，占位换来的是
             入口位置稳定（不会因为导入/删除某站点而忽隐忽现）。
             origins 是「忽略 origin 条件」汇总出来的，所以筛着某站点、又删掉它最后一条资产时
             该桶会从选项里消失：控件仍在，手动清空即可，不会出现「筛选生效却无从清除」。 -->
        <el-select
          v-model="filters.origin"
          class="f-select f-select--origin"
          size="small"
          placeholder="全部来源"
          clearable
          @change="load"
        >
          <el-option
            v-for="o in origins"
            :key="o.server"
            :label="`${o.label}（${o.count}）`"
            :value="o.server || 'local'"
          />
        </el-select>
        <el-input
          v-model="filters.q"
          class="f-search"
          size="small"
          clearable
          placeholder="搜索名称 / 说明，回车搜索"
          @keyup.enter="load"
          @clear="load"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-button size="small" :icon="Upload" @click="openImport">导入资产包</el-button>
        <el-button size="small" :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <div v-show="tab === 'queue'" class="split">
      <div class="list-pane">

        <!-- 卡片而不是表格：一行一条的窄列在 460px 详情旁边本来就装不下，
             卡片能把「名称 / 类型 / 状态 / 来源 / 时间」压成三行，扫得更快 -->
        <div v-loading="loading" class="cards">
          <!-- 按状态分块：待审在最上、草稿沉底（顺序沿用 STATUS_META），空组不渲染。
               组头吸顶 —— 滚到中间也知道自己在看哪一档；组内保持后端「更新时间倒序」的相对次序 -->
          <section v-for="g in groups" :key="g.status" class="status-group">
            <!-- 组头就是开关：整条可点、箭头指示展开态；收起后这一块只剩组头 -->
            <button
              type="button"
              class="sg-head"
              :class="{ 'is-collapsed': isCollapsed(g.status) }"
              :aria-expanded="!isCollapsed(g.status)"
              @click="toggleGroup(g.status)"
            >
              <el-icon class="sg-arrow"><ArrowRight /></el-icon>
              <span class="sg-dot" :class="'sg-dot--' + g.status"></span>
              <span class="sg-title">{{ g.label }}</span>
              <span class="sg-count">{{ g.rows.length }}</span>
              <span class="sg-line"></span>
            </button>
            <div v-show="!isCollapsed(g.status)" class="sg-cards">
              <div
                v-for="row in g.rows"
                :key="row.id"
                class="asset-card"
                :class="{ 'is-active': selected && selected.id === row.id }"
                @click="select(row)"
              >
                <div class="ac-main">
                  <div class="ac-top">
                    <span class="kind" :class="'kind--' + row.kind">{{ kindLabel(row.kind) }}</span>
                    <span class="ac-name" :title="row.display_name || row.name">
                      {{ row.display_name || row.name }}
                    </span>
                    <span class="muted ac-ver">v{{ row.build }}</span>
                  </div>
                  <div class="ac-sub mono">{{ row.name }}</div>
                  <!-- 说明一行：审核第一眼判断「这是不是它声称的那个东西」靠的就是这句，
                       放不下就省略，全文留在 title 里 -->
                  <div class="ac-desc" :title="row.description || ''">
                    {{ row.description || '未填说明' }}
                  </div>
                  <div class="ac-meta">
                    <el-tag size="small" :type="statusType(row.status)" effect="light">
                      {{ statusLabel(row.status) }}
                    </el-tag>
                    <span class="ac-dot">·</span>
                    <span>{{ listerLabel(row) }}</span>
                    <span class="ac-dot">·</span>
                    <span>{{ fmtTime(row.updated_at) }}</span>
                  </div>
                </div>

                <!-- 动作悬浮在卡片右侧：hover 才淡入，且只渲染当前状态真能点的那些 —— 灰着的图标
                     既不能点又占位置，不如直接不出现（状态机在后端收口，这里只是照着状态的可用集画） -->
                <div
                  v-if="row.file_count || ['pending', 'listed', 'delisted'].includes(row.status)"
                  class="ac-actions"
                  @click.stop
                >
                  <el-tooltip v-if="row.file_count" content="导出包" placement="top" :show-after="300">
                    <button class="ac-btn" :disabled="exportingId === row.id" @click="exportPackage(row)">
                      <el-icon><Download /></el-icon>
                    </button>
                  </el-tooltip>
                  <template v-if="row.status === 'pending'">
                    <el-tooltip content="通过" placement="top" :show-after="300">
                      <button
                        class="ac-btn ac-btn--ok"
                        :disabled="reviewingKey === 'approve:' + row.id"
                        @click="approve(row)"
                      >
                        <el-icon><Check /></el-icon>
                      </button>
                    </el-tooltip>
                    <el-tooltip content="驳回" placement="top" :show-after="300">
                      <button
                        class="ac-btn ac-btn--danger"
                        :disabled="reviewingKey === 'reject:' + row.id"
                        @click="reject(row)"
                      >
                        <el-icon><Close /></el-icon>
                      </button>
                    </el-tooltip>
                  </template>
                  <el-tooltip
                    v-else-if="row.status === 'listed'"
                    content="下架（撤出市场，已装副本不受影响）"
                    placement="top"
                    :show-after="300"
                  >
                    <button
                      class="ac-btn"
                      :disabled="reviewingKey === 'delisted:' + row.id"
                      @click="delisted(row)"
                    >
                      <el-icon><Bottom /></el-icon>
                    </button>
                  </el-tooltip>
                  <!-- 删除不要求先下架：下架是「留档撤出」，删除是「清库」，两者是并列手段
                       而不是前后步骤，所以在架与已下架都直接给删除。租户还没走完的档位
                       （pending / rejected / draft）后端仍会拒，这里也就不画。
                       删除不可恢复，所以要等的这一下必须有反馈：图标换成转圈、tooltip 改口
                       「删除中…」，否则点完只是静止变淡，看着像没点上 -->
                  <el-tooltip
                    v-if="['listed', 'delisted'].includes(row.status)"
                    :content="deletingId === row.id ? '删除中…' : '删除（不可恢复）'"
                    placement="top"
                    :show-after="300"
                  >
                    <button
                      class="ac-btn ac-btn--danger"
                      :class="{ 'is-busy': deletingId === row.id }"
                      :disabled="deletingId === row.id"
                      @click="removeAsset(row)"
                    >
                      <el-icon :class="{ 'is-loading': deletingId === row.id }">
                        <Loading v-if="deletingId === row.id" />
                        <DeleteIcon v-else />
                      </el-icon>
                    </button>
                  </el-tooltip>
                </div>
              </div>
            </div>
          </section>
          <el-empty v-if="!items.length && !loading" description="没有符合条件的资产" />
        </div>

        <div class="lp-foot">
          后端不分页（资产在百级）：默认「待审优先 + 更新时间倒序」，先在队列里裁决再进去看细节
        </div>
      </div>

      <section class="detail-pane">
        <el-empty v-if="!selected" description="左侧选择一个资产" />

        <template v-else>
          <div class="d-head">
            <div class="d-title">
              <span class="d-name">{{ selected.display_name || selected.name }}</span>
              <span class="mono d-schema">{{ selected.name }}</span>
              <el-tag size="small" :type="statusType(selected.status)">{{ statusLabel(selected.status) }}</el-tag>
              <span class="ver">v{{ selected.build }}</span>
            </div>
            <el-button link size="small" @click="loadDetail(selected.id)">刷新</el-button>
          </div>
          <!-- 裁决说明挂在标题下面（跟当前状态强相关），不占底部操作区的高度 -->
          <div class="d-hint">{{ actionHint }}</div>

          <div class="meta-grid">
            <div class="meta-cell">
              <div class="meta-label">类型</div>
              <div class="meta-value">{{ kindLabel(selected.kind) }}</div>
            </div>
            <div class="meta-cell">
              <div class="meta-label">{{ selected.origin ? '来源站点' : '提供方' }}</div>
              <div class="meta-value">{{ selected.origin ? selected.origin.server : listerLabel(selected) }}</div>
            </div>
            <div class="meta-cell">
              <div class="meta-label">已安装</div>
              <div class="meta-value">{{ installCount }}</div>
            </div>
            <div class="meta-cell">
              <div class="meta-label">更新时间</div>
              <div class="meta-value">{{ fmtTime(selected.updated_at) }}</div>
            </div>
          </div>

          <!-- 内容：审核第一眼要判断「这是不是它声称的那个东西」 -->
          <div class="blk">
            <div class="blk-head">
              <span class="blk-title">内容</span>
            </div>
            <div class="blk-body">
              <div class="desc">{{ selected.description || '提供方没有填描述' }}</div>
              <div class="kv">
                <span class="k">入口文件</span>
                <span class="v mono">{{
                  selected.entry || (selected.kind === 'workflow' ? 'FLOW.json（工作流定义）' : '（自定义工具由 TOOL.json 决定）')
                }}</span>
              </div>
              <div class="kv">
                <span class="k">文件 / 体积</span>
                <span class="v">{{ selected.file_count }} 个 · {{ fmtSize(selected.total_size) }}</span>
              </div>
              <div v-if="selected.origin" class="kv">
                <span class="k">来源</span>
                <span class="v">
                  从 {{ selected.origin.server }} 导入<template v-if="selected.origin.imported_at"> · {{ fmtTime(selected.origin.imported_at) }}</template>
                  <span v-if="selected.origin.asset_id" class="hint mono">（对方 id {{ selected.origin.asset_id }}）</span>
                </span>
              </div>
            </div>
          </div>

          <!-- 依赖：审核五项里的前三项都在这一块（依赖是否可得 / 能力要求是否相符 / 是否触顶） -->
          <div class="blk">
            <div class="blk-head">
              <span class="blk-title">依赖声明</span>
              <span class="hint">技能按名字匹配目标租户、工具按 ref 重建授权</span>
            </div>
            <div class="blk-body">
              <template v-if="hasDeps">
                <div v-if="deps.skills?.length" class="dep-row">
                  <span class="k">技能</span>
                  <span class="tags">
                    <span v-for="s in deps.skills" :key="s" class="tag">{{ s }}</span>
                  </span>
                </div>
                <div v-if="deps.private_skills?.length" class="dep-row">
                  <span class="k">私有技能</span>
                  <span class="tags">
                    <span v-for="s in deps.private_skills" :key="s" class="tag tag--warn">{{ s }}</span>
                  </span>
                  <span class="dep-note">不随资产迁移：装到目标租户后这些技能不存在</span>
                </div>
                <div v-if="deps.tools?.length" class="dep-row">
                  <span class="k">工具授权</span>
                  <span class="tags">
                    <span v-for="t in deps.tools" :key="t.type + ':' + t.ref" class="tag">
                      {{ t.ref }}<span v-if="t.grant_kind" class="tag-sub">{{ t.grant_kind }}</span>
                    </span>
                  </span>
                </div>
                <div v-if="deps.requires?.length" class="dep-row">
                  <span class="k">能力要求</span>
                  <span class="tags">
                    <span v-for="r in deps.requires" :key="r" class="tag tag--cap">{{ r }}</span>
                  </span>
                  <span class="dep-note">目标租户没开通这项能力时，装上也会拒跑</span>
                </div>
                <div v-if="deps.egress" class="dep-row">
                  <span class="k">出网</span>
                  <span class="tags"><span class="tag tag--cap">net.egress</span></span>
                </div>
              </template>
              <div v-else class="hint">没有声明任何依赖</div>
            </div>
          </div>

          <!-- 可见范围：allowlist 是「有行 = 允许」，没有行 = 谁都装不了 -->
          <div class="blk">
            <div class="blk-head">
              <span class="blk-title">可见范围</span>
              <span class="hint">只影响能不能装，已装出去的副本不受影响</span>
            </div>
            <div class="blk-body">
              <el-radio-group v-model="visForm.visibility" size="small">
                <el-radio value="all">所有租户</el-radio>
                <el-radio value="allowlist">指定租户</el-radio>
              </el-radio-group>
              <el-select
                v-if="visForm.visibility === 'allowlist'"
                v-model="visForm.schemas"
                multiple
                filterable
                collapse-tags
                collapse-tags-tooltip
                placeholder="选择可见租户"
                style="width: 100%; margin-top: 10px"
              >
                <el-option v-for="t in tenants" :key="t.schema" :label="tenantLabel(t)" :value="t.schema" />
              </el-select>
              <div class="row">
                <span class="hint">
                  {{ visForm.visibility === 'allowlist'
                    ? '白名单是「有行才可见」：一个都不勾等于谁都装不了'
                    : '缺省口径：所有已通过审核的资产对全部租户开放' }}
                </span>
                <span class="spacer" />
                <el-button size="small" :disabled="!visDirty" :loading="savingVis" @click="saveVisibility">
                  保存可见范围
                </el-button>
              </div>
            </div>
          </div>

          <div class="blk">
            <div class="blk-head">
              <span class="blk-title">审核意见</span>
              <span class="hint">提供方在「我的资产」里能看到这段文字</span>
            </div>
            <div class="blk-body">
              <div class="desc">{{ selected.review_note || '（还没有留下意见）' }}</div>
            </div>
          </div>
        </template>
      </section>
    </div>

    <!-- 跨服务器搬运（手工包）：把另一台机器导出的 zip 传进来。
         导入一律落「待审核」—— 对面审过不等于本机放行，收下后仍要在左边队列里过一眼。 -->
    <el-dialog v-model="importVisible" title="导入资产包" width="560px">
      <div class="import-form">
        <div class="import-hint">
          在另一台服务器的「本地市场 → 导出包」拿到 zip，在这里上传。包内文件会重新校验
          （路径 / 体积 / 清单 / sha256），受保护名与凭据会再筛一遍；换一台机器也能装。
          同一来源已有同名资产时会先问一句，确认即覆盖（本地快照 +1，重新进入待审）。
        </div>

        <div class="import-row">
          <label class="import-label">资产包</label>
          <div class="import-control">
            <input class="file-input" type="file" accept=".zip,application/zip" @change="onImportFileChange" />
            <div v-if="importForm.file" class="hint">
              已选：{{ importForm.file.name }}（{{ fmtSize(importForm.file.size) }}）
            </div>
          </div>
        </div>

        <div class="import-row">
          <label class="import-label">来源站点</label>
          <div class="import-control">
            <el-input v-model="importForm.origin" size="small" placeholder="留空则用包内记录的服务器名" />
            <div class="hint">
              只做溯源展示与去重（同一来源的同类同名资产只能有一条）；填个能认出来的名字，比如 <code>test</code>
            </div>
          </div>
        </div>

        <div v-if="importResult" class="import-result">
          <el-alert
            type="success"
            :closable="false"
            show-icon
            :title="`已导入：${importResult.asset?.name} v${importResult.asset?.build}（${importResult.overwritten ? '覆盖' : '新增'}，待审核）`"
          />
          <ul v-if="importResult.warnings?.length" class="warn-list">
            <li v-for="(w, i) in importResult.warnings" :key="i">{{ w }}</li>
          </ul>
          <div class="hint">导入的资产默认「待审核」，在左侧队列里通过后，租户市场才能看到它。</div>
        </div>
      </div>
      <template #footer>
        <template v-if="importResult">
          <el-button type="primary" @click="importVisible = false">知道了</el-button>
        </template>
        <template v-else>
          <el-button @click="importVisible = false">取消</el-button>
          <el-button type="primary" :loading="importing" :disabled="!importForm.file" @click="submitImport">
            导入
          </el-button>
        </template>
      </template>
    </el-dialog>

    <!-- 上架审核策略：租户上架资产要不要先过平台审核。
         缺省是「上架免审」（上架即对市场可见），需要把关的租户显式覆盖成「必须审核」。
         与「从远端拉取」的审核无关 —— 那条在对端的 install_policy 上。 -->
    <div v-show="tab === 'policy'" class="policy-pane">
      <div class="pp-card pp-card--global">
        <div class="pp-head">
          <span class="blk-title">全局默认</span>
          <span class="hint">只影响之后的上架，不追溯已经上架的内容</span>
        </div>
        <div class="pp-body">
          <el-switch
            v-model="policyDefault"
            active-text="上架需审核"
            inactive-text="上架免审"
            @change="savePolicyDefault"
          />
          <div class="hint pp-note">
            免审的租户上架后<strong>即时对市场可见</strong>（不再进待审队列）。从远端市场拉取的审核由对端的「导入策略」决定，与此无关。
          </div>
        </div>
      </div>

      <div class="pp-card">
        <div class="pp-head">
          <span class="blk-title">按租户覆盖</span>
          <span class="hint">共 {{ policyTenants.length }} 个租户 · 「跟随默认」不落行</span>
        </div>
        <!-- 卡片而不是表格：每行只有一个下拉，列宽对齐反而显得空；
             卡片左侧色条直接标出当前策略（灰=跟随默认 / 橙=必须审核 / 绿=免审） -->
        <div class="pp-body pp-body--grid">
          <div
            v-for="t in policyTenants"
            :key="t.schema"
            class="pp-tenant"
            :class="'pp-tenant--' + tenantPolicyValue(t.schema)"
          >
            <!-- tenantView 的字段是 schema / display_name（不是 schema_name）；
                 没有显示名时只留 schema 一次，不重复两遍 -->
            <div class="pp-tenant-title">
              <span class="pp-tenant-name" :title="t.display_name || t.schema">
                {{ t.display_name || t.schema }}
              </span>
              <span v-if="t.display_name" class="mono pp-tenant-schema">{{ t.schema }}</span>
            </div>
            <el-select
              :model-value="tenantPolicyValue(t.schema)"
              size="small"
              style="width: 100%; margin-top: 8px"
              @change="(v) => saveTenantPolicy(t.schema, v)"
            >
              <el-option label="跟随默认" value="inherit" />
              <el-option label="必须审核" value="review" />
              <el-option label="免审" value="noreview" />
            </el-select>
          </div>
          <el-empty v-if="!policyTenants.length" description="还没有租户" />
        </div>
      </div>
    </div>

    <!-- 对外接入：匿名访问开关 + 出口令牌（别人拿令牌读本机市场）。
         与「上架策略」互补：那边决定资产能不能上架，这边决定上架后谁能读。 -->
    <div v-show="tab === 'access'" class="policy-pane">
      <div class="pp-card pp-card--global">
        <div class="pp-head">
          <span class="blk-title">匿名访问</span>
          <span class="hint">默认关闭：只允许持令牌的站点读取</span>
        </div>
        <div class="pp-body">
          <el-switch
            v-model="marketOpen"
            :loading="savingOpen"
            active-text="允许匿名访问（不校验令牌）"
            inactive-text="仅限持令牌的站点"
            @change="toggleMarketOpen"
          />
          <div class="hint pp-note">
            打开后<strong>任何知道本机地址的人</strong>都能读走已通过审核的资产目录与资产包，
            下面的令牌也就形同虚设；只建议内网或资产本来就打算公开时使用。
            带令牌但令牌无效的请求仍会被拒（写错的令牌不会因为开放而被放过）。
          </div>
        </div>
      </div>

      <div class="pp-card">
        <div class="pp-head">
          <span class="blk-title">出口令牌</span>
          <span class="hint">只读，只含已通过审核的资产；明文只在创建时显示一次</span>
        </div>
        <div class="pp-body">
          <div class="token-bar">
            <el-input v-model="tokenName" size="small" placeholder="给谁用，例如 prod-cluster" style="width: 240px" />
            <el-button size="small" type="primary" :loading="creatingToken" @click="createToken">生成令牌</el-button>
          </div>
          <el-alert
            v-if="plainToken"
            type="warning"
            :closable="false"
            show-icon
            style="margin: 10px 0"
            title="请立刻复制并交给对方：离开本页后不会再显示"
          >
            <div class="plain-token">{{ plainToken }}</div>
          </el-alert>
          <el-table :data="tokens" size="small" style="width: 100%">
            <el-table-column label="名称" prop="name" min-width="120" />
            <el-table-column label="状态" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="row.enabled ? 'success' : 'info'" effect="plain">
                  {{ row.enabled ? '启用' : '停用' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="最近使用" width="160">
              <template #default="{ row }">{{ row.last_used_at ? fmtTime(row.last_used_at) : '从未' }}</template>
            </el-table-column>
            <el-table-column label="操作" width="130">
              <template #default="{ row }">
                <el-button link type="primary" size="small" @click="toggleToken(row)">
                  {{ row.enabled ? '停用' : '启用' }}
                </el-button>
                <el-button link type="danger" size="small" @click="removeToken(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
          <div v-if="!tokens.length" class="hint" style="margin-top: 8px">还没有令牌</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  ArrowRight,
  Bottom,
  Check,
  Close,
  Delete as DeleteIcon,
  Download,
  Loading,
  Refresh,
  Search,
  Upload
} from '@element-plus/icons-vue'
import { api } from '../api'

const emit = defineEmits(['count', 'session-expired'])

const KIND_META = [
  { value: 'skill', label: '技能' },
  { value: 'custom_tool', label: '自定义工具' },
  { value: 'agent', label: '智能体' },
  { value: 'workflow', label: '工作流' }
]
// 类型做成 chip：多一个「全部」，与资产市场的药丸筛选同款
const KIND_FILTERS = [{ value: '', label: '全部' }, ...KIND_META]
const STATUS_META = [
  { value: 'pending', label: '待审核' },
  { value: 'listed', label: '已上架' },
  { value: 'rejected', label: '已驳回' },
  { value: 'delisted', label: '已下架' },
  { value: 'draft', label: '草稿' }
]
const KIND_LABEL = { skill: '技能', custom_tool: '自定义工具', agent: '智能体', workflow: '工作流' }
// 状态词与资产域动作词对齐（list/delist）：listed 一律叫「已上架」，
// 不写「已通过」—— 那是审核动作的结果，同一个状态在组头与卡片上不能两种说法
const STATUS_LABEL = {
  draft: '草稿',
  pending: '待审核',
  listed: '已上架',
  rejected: '已驳回',
  delisted: '已下架'
}

const kindLabel = (k) => KIND_LABEL[k] || k || '—'
const statusLabel = (s) => STATUS_LABEL[s] || s || '—'
const statusType = (s) => {
  if (s === 'listed') return 'success'
  if (s === 'rejected') return 'danger'
  if (s === 'pending') return 'warning'
  return 'info'
}

const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '—')
const fmtSize = (n) => {
  const v = Number(n) || 0
  if (v < 1024) return `${v} B`
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`
  return `${(v / 1024 / 1024).toFixed(1)} MB`
}
const listerLabel = (a) => {
  // 导入行的「提供方」在别的机器上，本机只认来源站点名
  if (a?.origin?.server) return `来源：${a.origin.server}`
  const user = a?.listed_by?.user?.display_name || ''
  const tenant = a?.listed_by?.tenant?.display_name || a?.listed_by?.tenant?.schema || ''
  if (user && tenant) return `${user} · ${tenant}`
  return tenant || user || '—'
}

const items = ref([])
const loading = ref(false)
const filters = reactive({ kind: '', origin: '', q: '' })
const origins = ref([])

const selected = ref(null)
const detail = ref(null)
// 卡片上的悬浮动作按「动作:资产 id」记，避免 A 卡在转圈时 B 卡也跟着转
const reviewingKey = ref('')
const savingVis = ref(false)
const exportingId = ref('')

// 跨服务器导入（手工包）
const importVisible = ref(false)
const importing = ref(false)
const importResult = ref(null)
const importForm = reactive({ file: null, origin: '' })

const tenants = ref([])
const visForm = reactive({ visibility: 'all', schemas: [] })
// 载入时的快照：用来判断可见范围是否被改过（避免「没动过也能点保存」）
const visBaseline = ref('')

const pendingCount = computed(() => items.value.filter((i) => i.status === 'pending').length)

// 队列按状态分块：顺序沿用 STATUS_META（待审在最上、草稿沉底）。
// 只做归组不做排序 —— 组内保持后端「待审优先 + 更新时间倒序」的相对次序。
// 后端将来新增状态时落进「其它」，不会把行弄丢。
const groups = computed(() => {
  const buckets = new Map(STATUS_META.map((s) => [s.value, []]))
  const others = []
  for (const row of items.value) {
    const bucket = buckets.get(row.status)
    if (bucket) bucket.push(row)
    else others.push(row)
  }
  const out = STATUS_META.map((s) => ({ status: s.value, label: s.label, rows: buckets.get(s.value) }))
  if (others.length) out.push({ status: 'other', label: '其它', rows: others })
  return out.filter((g) => g.rows.length) // 空组不渲染
})

// 每块可收起：只记「收起来的那些」，默认全展开（后端将来新增的状态天然是展开的）。
// 用「换一个新 Set」而不是就地 add/delete —— 变更点只有一处，也不依赖集合的响应式代理
const collapsedGroups = ref(new Set())
const isCollapsed = (status) => collapsedGroups.value.has(status)
const toggleGroup = (status) => {
  const next = new Set(collapsedGroups.value)
  if (next.has(status)) next.delete(status)
  else next.add(status)
  collapsedGroups.value = next
}
// 选中的卡片若在收起的块里，等于「点了个寂寞」：导入后自动选中、或队列首条回落时都靠这句展开
const expandGroup = (status) => {
  if (!status || !collapsedGroups.value.has(status)) return
  const next = new Set(collapsedGroups.value)
  next.delete(status)
  collapsedGroups.value = next
}
const deps = computed(() => selected.value?.deps || {})
const hasDeps = computed(() => {
  const d = deps.value
  return !!(
    d.skills?.length ||
    d.private_skills?.length ||
    d.tools?.length ||
    d.requires?.length ||
    d.egress
  )
})
const installCount = computed(() => (detail.value?.install_count == null ? '—' : `${detail.value.install_count} 家`))
const visSnapshot = () => JSON.stringify({ v: visForm.visibility, s: [...visForm.schemas].sort() })
const visDirty = computed(() => visBaseline.value !== '' && visBaseline.value !== visSnapshot())

// 按钮为什么是灰的：直接把状态机的约束说出来，胜过去点一下再看报错
const actionHint = computed(() => {
  const s = selected.value?.status
  if (s === 'pending') return '通过后立即可被可见范围内的租户安装'
  if (s === 'listed') return '已上架：可下架（撤出市场，已装副本不受影响），也可直接删除（清库，不可恢复）'
  if (s === 'rejected') return '已驳回：提供方需要重新上架（版本 +1）后才会重新进入待审'
  if (s === 'delisted') return '已下架：不再出现在任何租户的资产市场里；可删除（不可恢复）'
  return '草稿尚未提交审核，不可裁决'
})

function handleError(err) {
  if (err?.status === 401) {
    emit('session-expired')
    return
  }
  ElMessage.error(err?.message || '请求失败')
}

const load = async () => {
  loading.value = true
  try {
    const data = await api.listAssets({
      kind: filters.kind || '',
      origin: filters.origin || '',
      q: filters.q.trim()
    })
    items.value = data?.items || []
    // 来源下拉由后端汇总（忽略 origin 条件），筛掉某个来源后其它选项仍在
    origins.value = data?.origins || []

    // 选中项跟着刷新后的行数据走；不在了就退回第一条，避免右侧停在一个已消失的资产上
    if (selected.value) {
      const hit = items.value.find((i) => i.id === selected.value.id)
      if (hit) {
        selected.value = hit
        // 裁决后 review_note 变了，详情要跟着重拉一次（列表里没有这段文字）
        loadDetail(hit.id)
      } else if (items.value.length) select(items.value[0])
      else clearSelection()
    } else if (items.value.length) {
      select(items.value[0])
    }

    // 顶栏徽标给「待审数」：只在没有任何筛选时才是全量口径，
    // 筛了就沿用上一次的值（否则徽标会随着筛选一起缩水，看着像「待审数变少了」）
    const unfiltered = !filters.kind && !filters.origin && !filters.q.trim()
    if (unfiltered) emit('count', pendingCount.value)
  } catch (err) {
    handleError(err)
  } finally {
    loading.value = false
  }
}

// chip 没有 @change，点完自己载入（下拉走模板上的 @change）
const pickKind = (v) => {
  filters.kind = v
  load()
}

const clearSelection = () => {
  selected.value = null
  detail.value = null
  visBaseline.value = ''
}

const select = (row) => {
  if (!row) return
  expandGroup(row.status) // 选中的行必须看得见：导入后自动选中、或选中项失效回落到首条时都可能落在收起的块里
  selected.value = row
  detail.value = null
  visForm.visibility = row.visibility === 'allowlist' ? 'allowlist' : 'all'
  visForm.schemas = []
  visBaseline.value = ''
  loadDetail(row.id)
}

// 详情接口比列表多带 manifest / install_count / allowlist，可见范围的编辑态从这里灌
const loadDetail = async (id) => {
  try {
    const a = await api.getAsset(id)
    if (!a || !selected.value || selected.value.id !== id) return
    detail.value = a
    visForm.visibility = a.visibility === 'allowlist' ? 'allowlist' : 'all'
    visForm.schemas = Array.isArray(a.allowlist) ? [...a.allowlist] : []
    visBaseline.value = visSnapshot()
  } catch (err) {
    handleError(err)
  }
}

const loadTenants = async () => {
  try {
    // page_size 缺省 0 = 不分页：白名单要多选，翻页取不全比慢更糟
    const data = await api.listTenants()
    tenants.value = data?.items || []
  } catch (err) {
    handleError(err)
  }
}
const tenantLabel = (t) => (t.display_name ? `${t.display_name}（${t.schema}）` : t.schema)

/**
 * 裁决一个资产。row 由卡片传入（接口只认 id + 动作，列表行够用）。
 * 状态判断留给调用处：禁用态靠模板里的 :disabled，这里不再兜一遍。
 */
const review = async (row, action, note) => {
  if (!row) return
  reviewingKey.value = `${action}:${row.id}`
  try {
    await api.reviewAsset(row.id, { action, note: note || '' })
    ElMessage.success(
      action === 'approve' ? '已通过，可见范围内的租户现在可以安装' : action === 'reject' ? '已驳回' : '已下架'
    )
    await load()
  } catch (err) {
    handleError(err)
  } finally {
    reviewingKey.value = ''
  }
}

const approve = async (row) => {
  if (!row) return
  try {
    const { value } = await ElMessageBox.prompt(
      `通过后「${row.name}」v${row.build} 可被可见范围内的租户安装。可以留一句给提供方看（可空）。`,
      '通过审核',
      {
        confirmButtonText: '通过',
        cancelButtonText: '取消',
        inputPlaceholder: '审核意见（可空）',
        inputValue: ''
      }
    )
    await review(row, 'approve', value)
  } catch {
    /* 取消 */
  }
}

// 驳回必须写原因：它就是租户侧「我的资产」里唯一能看到的那句话
const reject = async (row) => {
  if (!row) return
  try {
    const { value } = await ElMessageBox.prompt(
      '驳回原因会直接显示给提供方（租户控制台 → 资产市场 → 我的资产）。请说清楚改哪里再来。',
      `驳回「${row.name}」`,
      {
        confirmButtonText: '驳回',
        cancelButtonText: '取消',
        inputType: 'textarea',
        inputPlaceholder: '例如：依赖的 custom:report 工具没有一起上架',
        inputValidator: (v) => (v && v.trim() ? true : '请填写驳回原因')
      }
    )
    await review(row, 'reject', value)
  } catch {
    /* 取消 */
  }
}

const delisted = async (row) => {
  if (!row) return
  try {
    await ElMessageBox.confirm(
      `下架「${row.name}」？它会立刻从所有租户的资产市场消失；已经装出去的副本不受影响。`,
      '下架资产',
      { type: 'warning', confirmButtonText: '下架', cancelButtonText: '取消' }
    )
    await review(row, 'delist', '')
  } catch {
    /* 取消 */
  }
}

const saveVisibility = async () => {
  const a = selected.value
  if (!a) return
  savingVis.value = true
  try {
    const schemas = visForm.visibility === 'allowlist' ? visForm.schemas : []
    await api.setAssetVisibility(a.id, { visibility: visForm.visibility, schemas })
    ElMessage.success('可见范围已保存')
    visBaseline.value = visSnapshot()
    await load()
  } catch (err) {
    handleError(err)
  } finally {
    savingVis.value = false
  }
}

// ===== 二级 tab：全部资产 / 上架策略 / 对外接入 =====
// 队列用 v-show 保留：切到策略再切回来不该丢筛选条件和选中项
const tab = ref('queue')
const switchTab = (t) => {
  tab.value = t
  if (t === 'policy') loadListingPolicy()
  // 明文令牌只在当次可见：每次进来先清掉，避免切走再切回来还挂在屏幕上
  if (t === 'access') {
    plainToken.value = ''
    loadTokens()
    loadMarketSettings()
  }
}

// ===== 上架审核策略（全局默认 + 按租户覆盖）=====
// 三态：跟随默认（库里没有行）/ 必须审核（行 = true）/ 免审（行 = false）。
// 不存 inherit 值，避免出现「值是 inherit 但又带着一列」的矛盾行。
// 全局默认是「免审」，与后端的缺省一致（没有行 = 免审）。
const policyDefault = ref(false)
const policyOverrides = ref({}) // schema_name → 是否必须审核
const policyTenants = ref([])

const loadListingPolicy = async () => {
  try {
    const [data, t] = await Promise.all([api.getListingPolicy(), api.listTenants()])
    // 字段缺失也按免审（与后端「没有行 = 免审」同口径）
    policyDefault.value = data?.require_review === true
    const m = {}
    for (const o of data?.overrides || []) m[o.schema_name] = !!o.require_review
    policyOverrides.value = m
    policyTenants.value = t?.items || []
  } catch (e) {
    handleError(e)
  }
}

const tenantPolicyValue = (schema) => {
  if (!(schema in policyOverrides.value)) return 'inherit'
  return policyOverrides.value[schema] ? 'review' : 'noreview'
}

const savePolicyDefault = async (val) => {
  try {
    await api.setListingDefault(!!val)
    ElMessage.success(val ? '已改为：上架需审核' : '已改为：上架免审')
  } catch (e) {
    policyDefault.value = !val // 保存失败要把开关拨回去，否则界面在撒谎
    handleError(e)
  }
}

const saveTenantPolicy = async (schema, v) => {
  const require = v === 'inherit' ? null : v === 'review'
  try {
    await api.setListingOverride(schema, require)
    const m = { ...policyOverrides.value }
    if (v === 'inherit') delete m[schema]
    else m[schema] = require
    policyOverrides.value = m
    ElMessage.success('已保存')
  } catch (e) {
    handleError(e)
  }
}

// ===== 跨服务器搬运（手工包）=====
const openImport = () => {
  importForm.file = null
  importForm.origin = ''
  importResult.value = null
  importVisible.value = true
}

const onImportFileChange = (e) => {
  importForm.file = e.target.files?.[0] || null
}

// runImport 发一次导入请求。force=false 时若同来源已有同名资产，后端回
// 400 + data.existing；覆盖与否由下面那次确认决定，不再要求用户事先勾复选框
// （忘了勾只会得到一句「已存在」，还得重来一遍）。
const runImport = (force) =>
  api.importAssetPackage(importForm.file, { origin: importForm.origin.trim(), force })

const submitImport = async () => {
  if (!importForm.file) {
    ElMessage.warning('请选择资产包（.zip）')
    return
  }
  importing.value = true
  try {
    let data
    try {
      data = await runImport(false)
    } catch (err) {
      const existing = err?.data?.existing
      if (!existing) throw err
      // 撞名不是失败，而是「要你确认」：说清当前快照号与覆盖后的后果，确认就带 force 重试
      try {
        await ElMessageBox.confirm(
          `${err.message}。覆盖后本地快照升到 v${(existing.build || 0) + 1}，并重新进入待审；` +
            '已经装到租户里的副本不受影响。',
          '覆盖已有资产',
          { type: 'warning', confirmButtonText: '覆盖', cancelButtonText: '取消' }
        )
      } catch {
        return // 用户取消：当作没发生，不弹错误
      }
      data = await runImport(true)
    }
    importResult.value = data
    ElMessage.success(data?.overwritten ? '已覆盖导入，等待本机审核' : '已导入，等待本机审核')
    await load()
    // 选中刚导入的那条，顺手就能裁决
    const hit = data?.asset?.id ? items.value.find((i) => i.id === data.asset.id) : null
    if (hit) select(hit)
  } catch (err) {
    handleError(err)
  } finally {
    importing.value = false
  }
}

// 彻底删除：在架（listed）与已下架（delisted）都能删，不要求先下架（后端同样校验）。
// 快照、可见范围白名单与安装记录都会清掉且不可恢复 —— 所以确认框写清楚，
// 同时点明「已装到租户的副本不受影响」，避免以为会把别人装好的东西一起删了。
const deletingId = ref('')
const removeAsset = async (row) => {
  if (!row) return
  // 在架的直接删是「一步到位」的处置：得先说明它此刻还在所有租户的市场里，
  // 否则管理员容易以为这跟下架是同一件事（下架留档、删除清库）
  const onShelf = row.status === 'listed' ? '它还在架，删除后会立刻从所有租户的市场里消失。' : ''
  try {
    await ElMessageBox.confirm(
      `彻底删除「${row.display_name || row.name}」v${row.build}？` +
        onShelf +
        '快照、可见范围与安装记录都会清空，不可恢复；已经装到租户里的副本不受影响。',
      '删除资产',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  deletingId.value = row.id
  try {
    await api.deleteAsset(row.id)
    ElMessage.success('已删除')
    // load() 会把失效的选中项退回第一条，这里不用手动清
    await load()
  } catch (err) {
    handleError(err)
  } finally {
    deletingId.value = ''
  }
}

// 导出走 Blob + <a download>：包是二进制，JSON 那条 request() 用不了
const exportPackage = async (row) => {
  if (!row) return
  exportingId.value = row.id
  try {
    const { blob, filename } = await api.exportAssetPackage(row.id)
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = filename
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
    ElMessage.success(`已开始下载：${filename}`)
  } catch (err) {
    handleError(err)
  } finally {
    exportingId.value = ''
  }
}

// ===== 对外接入：匿名访问开关 + 出口令牌（2026-11 起从远端市场搬来）=====
// 方向说明：令牌是「别人拿它来读本机市场」，与远端市场的「对端站点（我去拉别人）」相反，
// 所以放在本地市场，和上架策略同页。
const tokens = ref([])
const tokenName = ref('')
const creatingToken = ref(false)
const plainToken = ref('')
const marketOpen = ref(false) // platform.market_settings.open，默认关
const savingOpen = ref(false)

const loadTokens = async () => {
  try {
    const data = await api.listMarketTokens()
    tokens.value = data?.items || []
  } catch (err) {
    handleError(err)
  }
}

const loadMarketSettings = async () => {
  try {
    const data = await api.getMarketSettings()
    marketOpen.value = !!data?.open
  } catch (err) {
    handleError(err)
  }
}

const toggleMarketOpen = async (val) => {
  if (val) {
    try {
      await ElMessageBox.confirm(
        '打开后，任何知道本机地址的人都能读取已通过审核的资产目录与资产包，不需要令牌。确定要开放吗？',
        '开放匿名访问',
        { type: 'warning', confirmButtonText: '确认打开', cancelButtonText: '取消' }
      )
    } catch {
      marketOpen.value = false // switch 已被 v-model 改过，取消要回滚
      return
    }
  }
  savingOpen.value = true
  try {
    await api.setMarketSettings(val)
    ElMessage.success(val ? '已开放匿名访问' : '已关闭匿名访问')
  } catch (err) {
    marketOpen.value = !val
    handleError(err)
  } finally {
    savingOpen.value = false
  }
}

const createToken = async () => {
  if (!tokenName.value.trim()) {
    ElMessage.warning('请填写令牌名（给谁用）')
    return
  }
  creatingToken.value = true
  try {
    const data = await api.createMarketToken(tokenName.value.trim())
    plainToken.value = data?.plain || ''
    tokenName.value = ''
    await loadTokens()
  } catch (err) {
    handleError(err)
  } finally {
    creatingToken.value = false
  }
}

const toggleToken = async (row) => {
  try {
    await api.setMarketToken(row.id, !row.enabled)
    await loadTokens()
  } catch (err) {
    handleError(err)
  }
}

const removeToken = async (row) => {
  try {
    await ElMessageBox.confirm(
      `删除令牌「${row.name}」？对方立刻失去读取本机市场的能力。`,
      '删除令牌',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    await api.deleteMarketToken(row.id)
    await loadTokens()
  } catch (err) {
    handleError(err)
  }
}

onMounted(async () => {
  await loadTenants()
  await load()
})
</script>

<style scoped>
/* ===== 三层灰阶（与租户管理 / 平台用户 / 插件与能力同款）=====
   大背景：#f0f2f5（与平台外壳同色，整页一块浅灰）
   两张灰卡：.list-pane / .detail-pane（#fafafa）
   资产卡 / .blk：白底 + 边框 + 投影 */
.section {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0;
  background: #f0f2f5;
  border-radius: 12px;
  box-sizing: border-box;
  overflow: hidden;
}

/* ===== 顶部一条：tab 在左，队列的筛选与操作贴右 =====
   两侧都要能换行（窄屏时右侧控件掉到下一行，tab 不被挤变形） */
.sub-bar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 12px;
  /* 顶部留 8px：这排按钮直接贴页面上沿太挤，往下降一点和左右两栏的 6px 内缩对齐。
     下方不留：间距交给 .split 自己的 6px 内缩，两处叠加会显得断层 */
  margin: 8px 0 0;
}

.sub-bar-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex-wrap: wrap;
  gap: 6px;
}

/* ===== 筛选控件：抄「资产市场 → 本地市场」那一套（.am-chip / .am-origin / .am-search）===== */
.filter-chip {
  height: 28px;
  padding: 0 12px;
  font-size: 13px;
  color: #666;
  background: #fff;
  border: 1px solid #e4e7ed;
  border-radius: 14px;
  cursor: pointer;
  transition: all 0.2s;
}

.filter-chip:hover {
  border-color: #1890ff;
  color: #1890ff;
}

.filter-chip.active {
  background: #e6f0f8;
  border-color: #b3d4f0;
  color: #1890ff;
  font-weight: 500;
}

/* 下拉与搜索固定宽度：选项文案长短不会把这一行挤变形 */
.f-select {
  width: 128px;
  flex-shrink: 0;
}

.f-select--origin {
  width: 150px;
}

.f-search {
  width: 168px;
  flex-shrink: 0;
}

/* ===== 二级 tab（全部资产 / 上架策略）=====
   与智能体列表的筛选 tab 同一套：浅灰胶囊、白色 2px 描边、选中加深，不用蓝底 */
.sub-tabs {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 4px;
}

/* 二级切换做成轻量药丸：小一号、无边框、选中是白底 + 蓝字，
   而不是原来的灰底块（灰底叠在 #f0f2f5 页面上几乎看不出选中） */
.sub-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 12px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: none;
  font-size: 13px;
  line-height: 20px;
  color: #606266;
  cursor: pointer;
  transition: background-color 0.2s, color 0.2s, border-color 0.2s;
}

.sub-tab:hover {
  background: #fff;
  color: #303133;
}

.sub-tab.is-active {
  background: #fff;
  border-color: #d9ecff;
  color: #1677d2;
  font-weight: 500;
  box-shadow: 0 1px 2px rgba(16, 24, 40, 0.06);
}

.sub-tab-badge {
  padding: 0 6px;
  border-radius: 9px;
  background: #f56c6c;
  color: #fff;
  font-size: 11px;
  line-height: 16px;
}

/* ===== 上架策略面板（全宽，代替原来的 560px 弹窗）===== */
.policy-pane {
  flex: 1;
  min-height: 0;
  overflow: auto;
  display: flex;
  flex-direction: column;
  gap: 10px;
  /* 上边 6px 与队列那一屏的 .split 内缩对齐（.sub-bar 已去掉下边距） */
  padding: 6px 6px 0 0;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.policy-pane::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.policy-pane::-webkit-scrollbar-track {
  background: transparent;
}

.policy-pane::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.policy-pane:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.policy-pane:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.pp-card {
  flex-shrink: 0;
  background: #fff;
  border: 1px solid #eef0f3;
  border-radius: 12px;
  box-shadow: 0 1px 3px rgba(16, 24, 40, 0.04);
  overflow: hidden;
}

.pp-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid #f2f3f5;
}

.pp-body {
  padding: 14px;
}

.pp-note {
  margin-top: 10px;
  line-height: 1.6;
}

/* ===== 对外接入：生成令牌一行 + 明文令牌 ===== */
.token-bar {
  display: flex;
  align-items: center;
  gap: 8px;
}

.plain-token {
  margin-top: 6px;
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  line-height: 1.6;
  color: #b26a00;
  word-break: break-all;
}

/* 说明文字压一档、区块标题反而提一档：靠字号差把「标题 / 内容」的层次拉开 */
.pp-card .blk-title {
  font-size: 14px;
}

/* 标题前的竖色条跟着长高，不然 14px 字配 12px 条会显得头重脚轻 */
.pp-card .blk-title::before {
  height: 14px;
}

.pp-card .hint {
  font-size: 11px;
}

.pp-card :deep(.el-switch__label) {
  font-size: 12px;
}

/* 「全局默认」这张只有一行开关 + 一句说明，文字比另一张再小一档，
   免得整块看着像正文段落 */
.pp-card--global :deep(.el-switch__label) {
  font-size: 11px;
}

.pp-card--global .pp-note {
  font-size: 10px;
}

/* 租户卡片：一行多张；左侧色条直接标出这条覆盖的性质 */
.pp-body--grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(168px, 1fr));
  gap: 8px;
  padding: 12px 14px 14px;
}

.pp-body--grid :deep(.el-empty) {
  grid-column: 1 / -1;
}

.pp-tenant {
  min-width: 0;
  padding: 9px 10px;
  background: #fafbfc;
  border: 1px solid #eef0f3;
  border-left: 3px solid #dcdfe6;
  border-radius: 8px;
}

.pp-tenant--review {
  border-left-color: #e6a23c;
}

.pp-tenant--noreview {
  border-left-color: #67c23a;
}

/* 名称与 schema 同一行：名称让位省略，schema 短且固定，不给它折行的机会 */
.pp-tenant-title {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}

.pp-tenant-name {
  min-width: 0;
  font-size: 12px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pp-tenant-schema {
  flex-shrink: 0;
  font-size: 11px;
}

.split {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px;
  padding: 6px 0;
}

/* ===== 左：资产列表灰卡（按状态分块）===== */
.list-pane {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  padding: 8px 8px 4px;
  box-sizing: border-box;
  overflow: hidden;
  background: #fafafa;
  border-radius: 12px;
}

/* 卡片队列：撑满剩余高度自己滚。
   只看纵向：每个分组容器整行占满（见 .status-group），列宽不在这里定，交给 .sg-cards。
   滚动条与右侧详情同款（位置常驻 6px、拇指透明 → 悬浮才上色），切换时内容不会横跳 */
.cards {
  flex: 1;
  min-height: 0;
  overflow: auto;
  display: grid;
  grid-template-columns: 1fr;
  align-content: start;
  gap: 8px;
  padding: 0;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.cards::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.cards::-webkit-scrollbar-track {
  background: transparent;
}

.cards::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.cards:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.cards:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.cards :deep(.el-empty) {
  grid-column: 1 / -1;
}

/* ===== 状态分块 =====
   .cards 仍是 auto-fill 网格；分组容器整行占满，列公式下放到 .sg-cards。
   不把 .cards 改成 flex：改一行就够，滚动与 .el-empty 的跨列规则都不用动 */
.status-group {
  grid-column: 1 / -1;
  min-width: 0;
}

/* 组头 = 吸顶 + 折叠开关。吸顶必须给底色，否则卡片会从下面透出来；
   它是 <button>，所以还要把浏览器默认的边框 / 字体 / 居中清掉 */
.sg-head {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 4px 0 6px;
  border: none;
  /* 吸顶要盖住滚上来的卡片，所以必须给实色；与灰卡同色才不会是一条白带 */
  background: #fafafa;
  font: inherit;
  text-align: left;
  cursor: pointer;
  user-select: none;
}

.sg-head:hover .sg-title {
  color: #1677d2;
}

/* 收起 / 展开共用一个箭头：展开时朝下（旋转 90°），收起时指回右 */
.sg-arrow {
  font-size: 12px;
  color: #c0c4cc;
  transform: rotate(90deg);
  transition: transform 0.2s ease;
}

.sg-head.is-collapsed .sg-arrow {
  transform: rotate(0deg);
}

/* 圆点与 statusType 同一套语义：橙=待审 / 绿=已通过 / 红=已驳回 / 灰=下架与草稿 */
.sg-dot {
  width: 6px;
  height: 6px;
  flex-shrink: 0;
  border-radius: 50%;
  background: #c0c4cc;
}

.sg-dot--pending {
  background: #e6a23c;
}

.sg-dot--listed {
  background: #67c23a;
}

.sg-dot--rejected {
  background: #f56c6c;
}

.sg-dot--delisted {
  background: #909399;
}

.sg-title {
  font-size: 12px;
  font-weight: 600;
  color: #303133;
}

.sg-count {
  padding: 0 5px;
  font-size: 11px;
  line-height: 15px;
  color: #909399;
  background: #f2f3f5;
  border-radius: 8px;
}

/* 一条淡线拉到右端：用线而不是外框标出块边界，避免白卡里再套一层框 */
.sg-line {
  flex: 1;
  height: 1px;
  background: #f2f3f5;
}

/* 列宽在这定：200px 打底，宽屏自动多排一列。
   卡片就三行正文，240px 起会让每张卡横向空出一截，扫一屏看到的资产反而更少 */
.sg-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  align-content: start;
  /* 资产卡内容比租户卡密，间距取 12px（其余列表页是 14px），太松会一屏看不完 */
  gap: 12px;
}

/* 一张卡三行：类型+名称+版本 / 资产键名 / 状态+提供方+时间。
   动作按钮绝对定位浮在右上角，不占卡片宽度（所以这里要 relative） */
.asset-card {
  position: relative;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 14px 16px;
  background: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 12px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  cursor: pointer;
  transition: all 0.3s ease;
}

.asset-card:hover {
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
}

.asset-card.is-active {
  border-color: #409eff;
  background: #ecf5ff;
  box-shadow: 0 2px 8px rgba(64, 158, 255, 0.18);
}

/* 卡内正文：占满左侧，动作区展开时靠 ellipsis 让位（不用绝对定位，免得压住文字） */
.ac-main {
  flex: 1;
  min-width: 0;
}

.ac-top {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.ac-name {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ac-ver {
  flex-shrink: 0;
  font-size: 12px;
}

.ac-sub {
  margin-top: 3px;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 说明行夹在键名（灰 mono）与元信息之间：颜色比键名深一档 ——
   两张小字同色会分不出主次，说明才是这一行里真正要读的 */
.ac-desc {
  margin-top: 3px;
  font-size: 12px;
  color: #606266;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ac-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
  font-size: 12px;
  color: #909399;
}

.ac-meta > span:not(.ac-dot) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ac-dot {
  flex-shrink: 0;
  color: #dcdfe6;
}

/* 悬浮动作区：绝对定位盖在卡片右上，宽度不参与布局 —— hover 才淡入，
   移开就消失；隐藏时 pointer-events: none，避免挡住卡片点击 */
/* 位置：贴着卡片右侧居中；视觉：与智能体卡片同款（24px 浅色胶囊，hover 填实）。
   外层不做白底遮罩 —— 胶囊自己是不透明的，挡住底下的字就够了 */
.ac-actions {
  position: absolute;
  top: 50%;
  right: 8px;
  transform: translateY(-50%);
  display: flex;
  align-items: center;
  gap: 4px;
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.2s ease;
}

.asset-card:hover .ac-actions {
  opacity: 1;
  pointer-events: auto;
}

/* 比智能体卡片那版更实：底色加深、边框和图标都用饱和色，再加一层薄影让它「浮」在卡上 */
.ac-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 1px solid #5aa2f0;
  border-radius: 6px;
  background: #dbeaff;
  color: #1677d2;
  cursor: pointer;
  box-shadow: 0 1px 3px rgba(22, 119, 210, 0.18);
  transition: all 0.2s;
}

.ac-btn .el-icon {
  font-size: 15px;
  color: inherit;
}

.ac-btn:hover {
  background: #409eff;
  border-color: #409eff;
  color: #fff;
  box-shadow: 0 2px 6px rgba(64, 158, 255, 0.35);
}

/* 通过 / 驳回用语义色：绿 = 放行，红 = 拒绝（后者与智能体卡片的删除按钮同款） */
.ac-btn--ok {
  background: #e2f5d8;
  border-color: #7fc95d;
  color: #3f9414;
  box-shadow: 0 1px 3px rgba(103, 194, 58, 0.22);
}

.ac-btn--ok:hover {
  background: #67c23a;
  border-color: #67c23a;
  color: #fff;
  box-shadow: 0 2px 6px rgba(103, 194, 58, 0.38);
}

.ac-btn--danger {
  background: #fde1e1;
  border-color: #f08a8a;
  color: #de3a3a;
  box-shadow: 0 1px 3px rgba(245, 108, 108, 0.22);
}

.ac-btn--danger:hover {
  background: #f56c6c;
  border-color: #f56c6c;
  color: #fff;
  box-shadow: 0 2px 6px rgba(245, 108, 108, 0.38);
}

/* 不可用的动作（状态不对 / 正在提交 / 包里没文件）：淡出且不吃鼠标 */
.ac-btn:disabled {
  opacity: 0.4;
  cursor: default;
  pointer-events: none;
}

/* 正在提交的那一个例外：保留满透明度，否则转圈图标被 0.4 压得看不见，
   就退化成「点了没反应」；鼠标仍不吃，防重复提交 */
.ac-btn.is-busy:disabled {
  opacity: 1;
  cursor: progress;
}

.lp-foot {
  flex-shrink: 0;
  padding-top: 8px;
  font-size: 12px;
  color: #c0c4cc;
}

/* ===== 右：详情灰卡（固定宽；灰卡上的 .blk 是白块） ===== */
.detail-pane {
  width: 460px;
  flex-shrink: 0;
  overflow: auto;
  padding: 10px 12px;
  box-sizing: border-box;
  background: #fafafa;
  border-radius: 12px;
  /* 滚动条：位置常驻（一直 6px），只是拇指透明 → 悬浮才上色，避免悬浮时内容横向跳动 */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.detail-pane::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.detail-pane::-webkit-scrollbar-track {
  background: transparent;
}

.detail-pane::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.detail-pane:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.detail-pane:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.d-head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.d-title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  flex-wrap: wrap;
}

.d-name {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.d-schema,
.ver {
  font-size: 12px;
  color: #909399;
}

.meta-grid {
  display: flex;
  gap: 26px;
  flex-wrap: wrap;
  margin-top: 14px;
  padding: 12px 0;
  border-top: 1px solid #f0f2f5;
  border-bottom: 1px solid #f0f2f5;
}

.meta-label {
  font-size: 12px;
  color: #909399;
  margin-bottom: 4px;
}

.meta-value {
  font-size: 14px;
  color: #303133;
}

/* ===== 折叠卡（与租户管理页同款 .blk，灰卡上靠白底立起来） ===== */
.blk {
  margin-top: 10px;
  background: #fff;
  border-radius: 12px;
  overflow: hidden;
  border: 1px solid #ebeef5;
}

.blk-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid #f2f3f5;
}

.blk-title {
  font-size: 13px;
  font-weight: 600;
  color: #303133;
}

.blk-body {
  padding: 10px 14px 14px;
}

.desc {
  font-size: 13px;
  color: #606266;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

.kv {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-top: 8px;
  font-size: 13px;
}

.kv .k {
  width: 76px;
  flex-shrink: 0;
  color: #909399;
  font-size: 12px;
}

.kv .v {
  color: #303133;
  word-break: break-all;
}

.dep-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 8px;
  font-size: 13px;
}

.dep-row .k {
  width: 76px;
  flex-shrink: 0;
  color: #909399;
  font-size: 12px;
}

.tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.tag {
  padding: 1px 8px;
  font-size: 12px;
  line-height: 18px;
  color: #606266;
  background: #f4f5f7;
  border-radius: 9px;
}

.tag--warn {
  color: #b26a00;
  background: #fdf6ec;
}

.tag--cap {
  color: #1874d0;
  background: #eef6ff;
}

.tag-sub {
  margin-left: 6px;
  color: #a8abb2;
}

.dep-note {
  font-size: 12px;
  color: #a8abb2;
}

.row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
}

.spacer {
  flex: 1;
}

/* 裁决说明：跟在标题下面，一行灰字。裁决动作已移到左侧卡片的悬浮按钮上 */
.d-hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.5;
  color: #a8abb2;
}

.kind {
  display: inline-block;
  padding: 1px 8px;
  font-size: 12px;
  line-height: 18px;
  border-radius: 9px;
  background: #f0f2f5;
  color: #606266;
  white-space: nowrap;
}

.kind--skill {
  background: #eef6ff;
  color: #1874d0;
}

.kind--custom_tool {
  background: #f4f0ff;
  color: #6b47c9;
}

.kind--agent {
  background: #effaf3;
  color: #2c9b62;
}

.kind--workflow {
  background: #fff7e6;
  color: #c47b00;
}

.mono {
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  color: #909399;
}

.muted {
  color: #c0c4cc;
}

.hint {
  font-size: 12px;
  color: #a8abb2;
}

/* ===== 导入资产包弹窗 ===== */
.import-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.import-hint {
  padding: 10px 12px;
  font-size: 12px;
  line-height: 1.6;
  color: #7a6a3d;
  background: #fdf6ec;
  border: 1px solid #faecd8;
  border-radius: 8px;
}

.import-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
}

.import-label {
  width: 76px;
  flex-shrink: 0;
  padding-top: 4px;
  font-size: 13px;
  color: #606266;
}

.import-control {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.import-control code {
  padding: 0 4px;
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  background: #f4f5f7;
  border-radius: 4px;
}

.file-input {
  max-width: 100%;
  font-size: 12px;
  color: #606266;
}

.import-result {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.warn-list {
  margin: 0;
  padding-left: 18px;
  font-size: 12px;
  line-height: 1.7;
  color: #7a6a3d;
  max-height: 180px;
  overflow: auto;
}

/* ==========================================================================
   视觉调整（覆盖上面的基础样式）
   目标：白卡不再「平铺一片」，队列更好扫、详情更有层次、审核动作不跑出视野。
   只改观感，不动结构。
   ========================================================================== */

/* 两张白卡：极轻阴影，让它们从灰底上「浮起来」一点 */
.list-pane,
.detail-pane {
  box-shadow: 0 1px 3px rgba(16, 24, 40, 0.04);
}

/* 表格：去外框、去竖线，表头变轻，行 hover 用淡蓝而不是默认灰 */
:deep(.el-table) {
  --el-table-border-color: transparent;
  --el-table-header-bg-color: transparent;
  --el-table-row-hover-bg-color: #f5faff;
}

:deep(.el-table::before),
:deep(.el-table--border::after),
:deep(.el-table__inner-wrapper::before) {
  display: none;
}

:deep(.el-table th.el-table__cell) {
  background: transparent;
  color: #a8abb2;
  font-weight: 500;
  font-size: 12px;
  border-bottom: 1px solid #eef0f3;
}

:deep(.el-table td.el-table__cell) {
  padding: 9px 0;
  border-bottom: 1px solid #f6f7f9;
}

/* 详情头：标题区与内容之间也拉一条线，避免整屏糊在一起 */
.d-head {
  padding-bottom: 12px;
  border-bottom: 1px solid #f2f3f5;
}

/* 元信息改成 2×2 小卡：横排一行灰字最难扫 */
.meta-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  margin-top: 12px;
  padding: 0;
  border: none;
}

.meta-cell {
  padding: 8px 10px;
  /* 详情区已是 #fafafa 灰卡，这里要白底才显得出小卡边界 */
  background: #fff;
  border: 1px solid #eef0f3;
  border-radius: 8px;
}

.meta-label {
  font-size: 11px;
  color: #a8abb2;
  margin-bottom: 3px;
}

.meta-value {
  font-size: 13px;
  font-weight: 500;
  color: #303133;
}

/* 区块：浅底 + 标题前一条竖色块。白卡里再套白卡会显得没有边界 */
.blk {
  background: #fafbfc;
  border: 1px solid #eef0f3;
  border-radius: 10px;
}

.blk-head {
  align-items: center;
  padding: 9px 12px;
  border-bottom: 1px solid #eef0f3;
}

.blk-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.blk-title::before {
  content: '';
  width: 3px;
  height: 12px;
  border-radius: 2px;
  background: #409eff;
}

.blk-body {
  padding: 10px 12px 12px;
}

/* 审核动作吸底：详情长的时候「通过 / 驳回」不该需要滚回去找。
   只留按钮一行（约 40px），说明文字已经移到标题下的 .d-hint。 */
/* 标签胶囊略收紧，和 12px 字号更配 */
.tag {
  border-radius: 6px;
}
</style>
