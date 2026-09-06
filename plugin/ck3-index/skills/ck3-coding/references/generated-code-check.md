# Check generated code before delivery

Read when generating or correcting runnable CK3 script, GUI, or localization, including a QQ code example. Quoting existing source or discussing pseudocode does not require manufacturing a validation request.

## Draft, check, repair, deliver

1. Prepare the intended code and its file context. Send `ck3_check` a `files` array of `{path, content}` with complete virtual file contents. The relative path selects the dialect; it is never opened. Do not send a real project path, request a dirty-file scan, or leave content implicit.
2. For a fragment, put it inside the smallest valid enclosing file/block for its intended use. Preserve the fragment unchanged within that wrapper and state any assumed caller scope or context. A wrapper check covers that context, not arbitrary placement elsewhere. Do not use placeholders as executable code merely to satisfy the parser.
3. Run the default syntax and static semantic checks; do not select `syntax_only=true` just to remove a semantic error. Read `structuredContent`: `passed`, total `errors`/`warnings`, each file's `coverage`, `limited`, and `diagnostics`. `isError=false` means the tool ran, not that the code passed.
4. Fix relevant errors while preserving the requested behavior, then resubmit the complete corrected texts. Use `ck3_check_rules key=...` only when an exact built-in command fact would resolve an uncertainty. An unknown helper is not automatically illegal. Read warnings even when `passed=true`; fix real issues and explain relevant remaining uncertainty.
5. Deliver only the final checked revision as ready-to-use code. `content_sha256` identifies each submitted UTF-8 text; keep the receipt associated with that revision. Any subsequent code change requires another check. A displayed fragment may omit only its declared test wrapper; it must not be silently edited after validation.

If diagnostics are truncated, narrow the checked file set or request the needed diagnostic limit. Error totals are computed before display truncation. A limited syntax check or an execution failure is not a complete pass. Stop unchanged retries when the same failure persists without a new correction; report the unresolved blocker and label any remaining code as a draft instead of claiming success.

Example request for an effect definition:

```json
{"files":[{"path":"common/scripted_effects/bot_example.txt","content":"bot_example = { add_gold = 25 }\n"}]}
```

The path is virtual and no file is created. The caller still needs to establish that the effect runs in an appropriate scope.

## Coverage and local integration

`ck3_check` is an independent service with no database, configuration, project files, or source visibility selector. Both local sessions and QQ can check their own submitted text. It uses compiled versioned contracts by default; read the reported rule source. `ck3_check_rules` describes that checker snapshot, while indexed engine-reference queries may have newer local evidence.

A successful result means no errors in the reported static coverage. Object/localization/resource references, cross-file overrides, dynamic scripts, and game execution remain unchecked. A local final patch preflight already covering the exact complete final texts can satisfy the check-before-delivery requirement; it additionally uses the authorized index. Do not duplicate that check without a reason. QQ must not use the private preflight/review tools.

If the checker is unavailable, report that automatic validation could not run. Do not guess its outcome or switch QQ into a private validator. A suitable final note is: “静态语法和已覆盖的规则检查通过；跨文件引用与实机运行尚未验证。” Mention remaining warnings when they affect use, rather than always showing a long checklist.
