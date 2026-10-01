import type { Chart } from 'chart.js'

/**
 * 图表文字的字体：与全站数字、正文同一套无衬线（Space Grotesk + Noto Sans SC）。
 * Chart.js 默认用 Helvetica Neue，不改的话图例、坐标轴、tooltip 的数字会和页面其余部分不是同一种字。
 * 这里写成字面量而不是读 CSS 变量：canvas 的 font 属性不认 var()。
 */
export const CHART_FONT_FAMILY =
  "'Space Grotesk Variable', 'Space Grotesk', 'Noto Sans SC', system-ui, -apple-system, 'PingFang SC', 'Microsoft YaHei', sans-serif"

/** 在图表组件注册完 Chart.js 后调用一次；幂等，设置的是全局默认值，所有图表同步生效。 */
export function applyChartTypography(chart: typeof Chart): void {
  chart.defaults.font.family = CHART_FONT_FAMILY
}
