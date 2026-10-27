/**
 * 图表文字的字体：与全站数字、正文同一套无衬线（Space Grotesk + Noto Sans SC）。
 * Chart.js 默认用 Helvetica Neue，不改的话图例、坐标轴、tooltip 的数字会和页面其余部分不是同一种字。
 * 通过图表自己的 options.font 传入（只作用于该图表，不改 Chart.defaults，后台图表不受影响）。
 * 这里写成字面量而不是读 CSS 变量：canvas 的 font 属性不认 var()。
 */
export const CHART_FONT_FAMILY =
  "'Space Grotesk Variable', 'Space Grotesk', 'Noto Sans SC', system-ui, -apple-system, 'PingFang SC', 'Microsoft YaHei', sans-serif"
