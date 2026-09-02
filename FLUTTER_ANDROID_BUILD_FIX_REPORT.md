# Flutter Android Release 构建兼容性修复报告

日期：2026-09-01
项目：`D:\codex-remote\app`

## 原始报错

```text
e: Language version 1.4 is no longer supported; please, use version 1.8 or greater.

FAILURE: Build failed with an exception.

Execution failed for task ':gradle:compileKotlin'.
```

## 根因

项目自身没有配置 `languageVersion = "1.4"`。审查结果如下：

- `android/settings.gradle` 原本使用 AGP `7.3.0`、Kotlin Gradle Plugin `1.8.22`。
- `android/app/build.gradle` 的 `kotlinOptions.jvmTarget` 已经是 `1.8`。
- `settings.gradle` 通过 `includeBuild(...)` 引入 Flutter SDK：
  `D:\flutter\packages\flutter_tools\gradle`。
- Flutter 3.47.2 bundled Gradle plugin 使用 Kotlin `2.2.20`，而 Gradle `7.6.3` 的 embedded Kotlin/Kotlin DSL 仍按 `1.4` 参数调用 Kotlin 编译器，实际参数中出现了：
  `-api-version 1.4 -language-version 1.4`。

因此原始 Kotlin 错误的来源是项目 Gradle wrapper 与当前 Flutter SDK bundled Gradle plugin 不兼容，不是业务代码，也不是 Flutter SDK 中某个业务文件写入了 1.4。本次没有修改 Flutter SDK。

在修复该首因后，构建继续暴露了几处与 Flutter 3.47.2/Dart 3.13.2 的旧插件兼容问题，按一次一个根因依次修复：

1. `pinenacl 0.5.1`、`win32 5.5.0` 使用已移除的 `UnmodifiableUint8ListView`。
2. `flutter_secure_storage_windows 3.1.2` 与 `win32 6.x` API 不兼容；最终采用 `win32 5.5.4`，保留原 secure storage API。
3. `path_provider_android 2.3.1` 引入 `jni 1.0.3`，在中文 Windows 用户目录和跨盘路径下触发 CMake JSON `Invalid escape sequence`；锁定到 `2.2.18`，不引入 JNI native build。
4. `flutter_plugin_android_lifecycle 2.0.19`、`image_picker_android 0.8.12+1`、`video_player_android 2.4.14` 和 `speech_to_text 6.6.0` 仍引用已移除的 Flutter v1 embedding `PluginRegistry.Registrar`。
5. Kotlin 增量编译器在依赖源码位于 `C:`、项目位于 `D:` 时出现跨盘缓存路径异常；禁用 Kotlin incremental cache，并将 Gradle JVM 编码设为 UTF-8。

## 修改文件清单与内容

### `D:\codex-remote\app\android\gradle\wrapper\gradle-wrapper.properties`

- Gradle `7.6.3` → `8.14`。
- 原因：避免 Gradle 7.6 embedded Kotlin/Kotlin DSL 注入 language version 1.4；这是当前 Flutter bundled plugin 支持带的最低可用 Gradle 版本。
- 影响：构建工具链升级，继续要求 JDK 17；未改业务代码。

### `D:\codex-remote\app\android\settings.gradle`

- AGP `7.3.0` → `8.11.1`。
- Kotlin Gradle Plugin `1.8.22` → `2.2.20`。
- 原因：与当前 Flutter 3.47.2 bundled Gradle plugin 的兼容版本带对齐。
- 影响：仅影响 Android 构建工具，不改变运行时协议或业务逻辑。

### `D:\codex-remote\app\android\gradle.properties`

- `org.gradle.jvmargs` 增加 `-Dfile.encoding=UTF-8`。
- 增加 `kotlin.incremental=false`。
- 原因：修复中文用户目录/跨盘依赖路径导致的编译器路径转义和增量缓存问题。
- 影响：Kotlin 编译可能变慢；APK 功能和字节码语义不因该设置改变。

### `D:\codex-remote\app\pubspec.yaml`

- `pinenacl`：`^0.5.1` → `^0.6.0`。
- `image_picker`：`^1.1.0` → `^1.2.3`。
- `video_player`：`^2.8.0` → `^2.14.0`。
- `speech_to_text`：`^6.6.0` → `^7.4.0`。
- 增加传递依赖锁定：
  - `win32: 5.5.4`
  - `path_provider_android: 2.2.18`
  - `flutter_plugin_android_lifecycle: 2.0.35`
- `flutter_secure_storage` 保持 `^9.2.2`，最终锁定版本仍为 `9.2.4`，没有修改 `secure_store.dart` 或 Token 存储 API。

### `D:\codex-remote\app\pubspec.lock`

- 由 `flutter pub get` 根据上述约束自动更新。
- 最终关键版本：`pinenacl 0.6.0`、`win32 5.5.4`、`path_provider_android 2.2.18`、`flutter_plugin_android_lifecycle 2.0.35`、`image_picker_android 0.8.13+21`、`video_player_android 2.12.1`、`speech_to_text 7.4.0`。

未修改 Codex Remote 的 WebSocket、Bridge、FRP、Token 协议或业务实现文件。

## 构建命令

使用 Temurin JDK `17.0.20.1`、Flutter `3.47.2`、Dart `3.13.2`，依次执行：

```text
flutter clean
flutter pub get
flutter build apk --release
```

## 最终构建结果

```text
BUILD_EXIT=0
√ Built build\\app\\outputs\\flutter-apk\\app-release.apk (67.1MB)
```

APK 已实际存在：

`D:\codex-remote\app\build\app\outputs\flutter-apk\app-release.apk`

- 文件大小：`70,391,954` bytes
- SHA-256：`CE5078854104EA931AF8EA63023CDA2E5C2143D80770BF4A0C50F82831144C9F`

构建过程中 Android SDK 自动补齐了 Build-Tools 35、Platform 33/34/35 和 CMake 3.22.1；这些是构建环境组件，不是项目业务修改。

## 尚存风险

- Flutter 当前给出未来兼容性警告：建议后续升级到 Gradle 至少 `9.1.0`、AGP 至少 `9.0.1`、Kotlin 至少 `2.3.20`。本次按 MVP 原则停在已验证可构建的最低兼容带。
- `dependency_overrides` 是针对当前 Flutter/Dart 与 Windows 中文路径环境的稳定性锁定；升级相关插件时应重新验证并尽量移除这些 override。
- 本次只完成 Release APK 构建验证，没有在实体设备上执行运行时、扫码、语音、视频播放或安全存储回归测试。
- `flutter_markdown` 当前被 pub 标记为 discontinued；本次未处理，因为它不是本次构建失败根因。

## 后续升级建议

不建议在本次 MVP 修复中继续批量升级。当前版本已经成功构建，但建议后续单独安排一次 Android 工具链升级任务，按 Flutter 新模板逐步评估 Gradle 9.x、AGP 9.x、Kotlin 2.3.x，并同步验证所有插件和设备运行时行为。
