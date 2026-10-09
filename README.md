# 已有草稿临时归档

此分支只存档已有内容。源码草稿尚未整合，各项原有未完成状态保持原样。

归档包含 212 份源码快照、219 个交付目录、75 份不同的本地 HEAD 树和 14 份暂存区改动。重复源码按 SHA-256 去重；已提交到 recovery 基准的字节只保存引用。缓存、日志、数据库、工具链和内部记录未上传，具体排除项在归档内的 exclusions.json。

## 下载与还原归档文件

archive/ 下的 25 个文件是真实二进制分片，不是 Base64 文本。按文件编号顺序连接，然后核对整个压缩包的 SHA-256：

```sh
cat archive/drafts-20261009T123136Z.tar.gz.part-* > drafts-20261009T123136Z.tar.gz
sha256sum -c ARCHIVE-SHA256SUMS
tar -xzf drafts-20261009T123136Z.tar.gz
```

这只会解出 draft-archive/ 资料目录，不会执行项目代码。每个归档文件还有 draft-archive/SHA256SUMS 可校验。恢复所选快照后，还需按根目录 RESTORATION-ADDENDUM.json 中同名快照的条目补回 .gitignore 和已有补丁引用；这些字节都已在基准或压缩包内，补充清单覆盖初版的对应排除标签。若主清单指向重复快照，仍使用最初选择的快照名称查补充清单。完整快照恢复流程见解压后的 README.md；下面的 snapshots/、git-heads/、blobs/ 等路径均位于解压后的 draft-archive/ 内。

根目录 CATALOG.md 可先查看所有草稿名称；archive-manifest.json 列出每个分片和整包校验值。

---

# Solsynth Go 临时草稿归档

本归档仅保存已有草稿，不整合、不合并、不执行项目代码或测试，也不表示任何草稿已通过验收。各目录代表不同时间或不同方案的状态，请勿依次应用所有补丁。

## 已确认已提交至本私有仓库的基准

- 仓库：momo-mnsjtxy/solsynth-go-staging
- Recovery commit：3570f0adf66231d1c211ec03a7bfa196d2f10178
- Recovery tree：b702b03d55cd41b6aa310517f5b8056c2c6aacf6
- 工作区保留的 corrected-962：本地 commit f22fbb439754383b5909e9d12223ea645020963e，tree ce464abc44805afd68ea552423e8f00141ce6e40；它是未发表的归档状态。
- main 和 recovery 分支不在本归档的修改范围内。

## 优先查找的最近草稿

| 草稿 | 源码状态 | 原始交付/补丁 |
|---|---|---|
| corrected-962 冻结状态 | snapshots/solsynth-go-continuation.json | git-heads/ce464abc44805afd68ea552423e8f00141ce6e40.json |
| Chat live fanout（暂停） | snapshots/chat-live-fanout.json | snapshots/chat-live-delivery.json；paused-work-in-progress.patch |
| Chat subscription sync（暂停） | snapshots/chat-subscription-sync.json | original-artifacts.json 中的 chat-sync-live-prerequisite.patch |
| Post pin | snapshots/solsynth-postpins.json、snapshots/post-pin-authority-review.json | snapshots/post-pin-delivery.json、snapshots/post-pin-authority-review-delivery.json；paused-correction.patch |
| Miniapp upload（未完成状态） | snapshots/miniapp-upload-flow.json、snapshots/miniapp-upload-authority-review.json | snapshots/miniapp-upload-delivery.json |
| 全部其他历史和独立草稿 | CATALOG.md | 每个条目链接到其准确来源清单 |

## 文件布局

- manifest.json：范围、数量、已确认基准和限制。
- CATALOG.md：所有源码快照和交付目录的索引。
- snapshots/*.json：每个原工作目录相对基准的独立状态，或原始补丁文件映射。
- git-heads/*.json：不同本地 HEAD 的已提交草稿树，防止工作目录后续编辑遮住旧状态。
- git-heads-index.json：HEAD 树索引及已有暂存区改动；未执行或提交这些改动。
- original-artifacts.json、supplemental-artifacts.json：根目录原始补丁、分组草稿、部分源码和冻结包内源码的原字节映射。
- metadata-references.json：来源文件中的 commit、base、补丁顺序与依赖文件引用；原始内部报告未上传。
- base-files.json：已发表基准树中的路径、Git blob 和 SHA-256；其已有内容不重复上传。
- blobs/sha256/*/*.blob：按 SHA-256 去重保存的原始字节；没有可自动运行的工作流路径。
- duplicates.json：相同字节和保留源码相同的快照映射。
- exclusions.json：明确排除的缓存、日志、数据库、工具链、内部记录等。
- frozen-containers.json：既有压缩包的成员目录；仅保存适用的源码成员。
- archival-screening.json：归档上传所需的最小路径/秘密筛查结果，非代码审查。

## 如何恢复某一份源码草稿

1. 在一个新的独立目录检出上面的 recovery commit，先验证 tree 与 b702b03d… 相同。保留原工作目录不动。
2. 只选一个 snapshots/<名称>.json。若有 duplicate_preserved_snapshot_of，先读取该清单；它表示保留的源码内容相同，不代表审查结论相同。
3. 对 changed_or_added 的每个目标路径，读取指定 blob 的原始字节，验证 bytes 和 SHA-256 后写入该相对路径；mode 保存了原文件模式。若条目使用 published_base_path，则从确认的 recovery commit 的该路径取回相同字节。不要把 .blob 后缀写进目标文件名。
4. absent_base_paths 表示该快照不存在的基准文件；仅在新的恢复目录删除这些路径。excluded_base_paths 是故意省略的条目，必须先看 exclusions.json，不能当作普通删除。
5. 不在 changed_or_added、absent_base_paths、excluded_base_paths 中的文件，使用确认的 recovery 基准内容。没有声称所有外部依赖和执行环境都已保存。
6. 独立的 git-heads/<tree>.json 使用相同办法恢复某个已提交的本地 HEAD。当前工作目录和暂存区的状态是不同资料，不要混为一次补丁应用。

原始补丁可按清单把 blob 字节另存回原文件名，验证 SHA-256 后保留待用。仅在人工确认相应 base 和依赖后选择性应用；本次归档没有应用任何补丁。所有历史 delta 都是各自相对统一 recovery 基准的替代快照，绝不是顺序补丁链。

所有工作流 YAML 均只存在于不生效的归档字节中。恢复项目源码是将来的独立动作；本分支不启动 CI，不启用自动合并或部署。
