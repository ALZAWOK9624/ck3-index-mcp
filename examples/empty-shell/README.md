# CK3 Index 空壳模组

这是一个不改变游戏机制的最小打包样例。`mod/` 里只有一个未被引用的本地化标记，用来证明本地化 BOM 修复和 CK3 加载目录收录工作正常；双描述文件、安装说明与清单均由打包器生成。

```powershell
ck3-index package-dir examples/empty-shell/mod --meta examples/empty-shell/metadata.json
```
