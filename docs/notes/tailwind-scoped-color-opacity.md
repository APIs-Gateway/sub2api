# Tailwind 透明度修饰符 vs `<style scoped>` 自定义色

`HomeView.vue` 的 Quiet Ledger 调色板（`--ink` / `--paper` / `--clay` / `--muted` / `--faint` / `--line` / `--card`）是在组件 `<style scoped>` 里用 CSS 变量定义的，**不是 Tailwind config 里注册的颜色**。

坑：对这类「scoped 自定义色」写 Tailwind 透明度修饰符（如 `text-ink/70`、`bg-clay/20`）会**静默失效** —— 不报错，但透明度完全不生效，颜色按 100% 渲染。因为 `/NN` 修饰符只对 Tailwind 知道的颜色生效，它不认识这些 scoped 变量。

## 怎么修

要弱化就**显式补写转义类**，用 `color-mix` 自己算：

```css
.text-ink\/70 { color: color-mix(in srgb, var(--ink) 70%, transparent); }
```

（本仓修复于 `8b180643`。）

全站其它视图用的是 Tailwind config 里注册的真实颜色，`/NN` 透明度修饰符正常工作 —— **这个坑只在 HomeView 这种 scoped 自定义色场景出现**。
