// 前端 ESLint 配置(flat config)。
//
// 为什么放在 web/ 根目录而不是每个包一份:
// pnpm 运行子包脚本时会把工作区根的 node_modules/.bin 加进 PATH,
// 且 ESLint 从**当前工作目录逐级向上**找配置 —— 放在根上,三个包共用一份,
// 规则改一处即可。配置里的插件从本文件所在目录解析,依赖装在根上就对。
//
// 规则取舍与 .golangci.yml 保持同样的思路:只开能真正挡住 bug 的,
// 不开会催生无意义改动的风格规则。

import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import pluginVue from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'
import globals from 'globals'

export default tseslint.config(
  {
    // 构建产物与依赖。dist 里是 Vite 打包后的产物,
    // 对它做类型/风格检查只会得到一堆噪音。
    ignores: ['**/dist/**', '**/node_modules/**'],
  },

  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/recommended'],

  // .vue 单文件组件的解析器接线。
  //
  // 必须显式写:vue-eslint-parser 只负责切分模板/脚本/样式,
  // 它**不认识 TypeScript**。不把 parserOptions.parser 指向
  // @typescript-eslint/parser 的话,<script setup lang="ts"> 里的
  // `interface`、类型标注、泛型全会被当成 JS 语法而报
  // "Parsing error: The keyword 'interface' is reserved"。
  //
  // 放在 tseslint 各项之后:flat config 按顺序合并,靠后的覆盖靠前的。
  {
    files: ['**/*.vue'],
    languageOptions: {
      parser: vueParser,
      parserOptions: {
        parser: tseslint.parser,
        sourceType: 'module',
        extraFileExtensions: ['.vue'],
      },
    },
  },

  {
    languageOptions: {
      // 浏览器环境。两个 SPA 都在浏览器里跑,没有 node 全局。
      globals: globals.browser,
    },
    rules: {
      // 终端用户站与管理后台都用 Ant Design Vue,组件名冲突由本地注册解决,
      // 组件名本身不该被当成未定义变量。
      'vue/multi-word-component-names': 'off',

      // 下面这组全是**纯排版**规则,与 .golangci.yml 里关掉 godot 是同一个理由:
      // 它们规定属性/内容/缩进必须怎么换行,但模板里「同类属性写在一行」
      // 恰恰是可读性更好的写法。强行换行只会让模板膨胀,
      // 并制造大量与逻辑无关的 diff。
      //
      // 这些模板确实存在几处缩进不齐(见 admin 的 AuditView / ClientsView),
      // 但那属于可以单独提一次格式化提交的事,不该混进 CI 修复里。
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/first-attribute-linebreak': 'off',
      'vue/html-closing-bracket-newline': 'off',
      'vue/html-indent': 'off',
      'vue/html-self-closing': 'off',

      // 与 Go 侧的 errorlint 同源:抛出的必须是 Error 而不是任意值。
      // 这条规则需要类型信息,而本项目的前端目前没开 typed linting
      // (shared 连 tsconfig 都没有),所以这里用不依赖类型的替代:
      // 禁止把非 Error 的值直接 throw。
      // 真正需要类型信息的 @typescript-eslint/only-throw-error 暂不启用,
      // 等前端全面 typecheck 之后再开。
      'no-throw-literal': 'error',

      // 未使用的变量留着是噪音,但「下划线前缀」是社区公认的放弃写法,
      // 放宽它比逼着人写 `void x` 诚实。
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
    },
  },

  // 测试文件放宽:测试里大量使用未使用参数与宽松断言是合理的。
  {
    files: ['**/*.test.ts'],
    rules: {
      '@typescript-eslint/no-unused-vars': 'off',
    },
  },
)