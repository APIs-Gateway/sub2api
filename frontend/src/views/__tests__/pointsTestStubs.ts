import { h } from 'vue'

// 邀请返利积分制（issue #11）前端视图测试共享 stub。
// 关键:DataTable / Pagination / 布局 stub 都「渲染对应插槽」,让视图模板里的单元格表达式
// （格式化、条件 class、徽章）与事件 handler 真正执行,从而被 v8 覆盖统计到。

export const AppLayoutStub = { template: '<div><slot /></div>' }

export const TablePageLayoutStub = {
  template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
}

// DataTable:对每行渲染每列的 cell-<key> 作用域插槽;无数据时渲染 empty 插槽;
// 另渲染 cell-actions(若提供)。暴露一个 sort 按钮以触发 @sort。
export const DataTableStub = {
  props: ['columns', 'data', 'loading', 'rowKey', 'serverSideSort'],
  emits: ['sort'],
  setup(props: any, { slots, emit }: any) {
    return () => {
      const rows = props.data ?? []
      const children: any[] = []
      if (!rows.length && slots.empty) {
        children.push(h('div', { 'data-test': 'empty' }, slots.empty()))
      }
      for (const row of rows) {
        const cells: any[] = []
        for (const col of props.columns ?? []) {
          const slot = slots[`cell-${col.key}`]
          if (slot) {
            cells.push(h('div', { 'data-test': `cell-${col.key}` }, slot({ row, value: row[col.key] })))
          }
        }
        if (slots['cell-actions']) {
          cells.push(h('div', { 'data-test': 'cell-actions' }, slots['cell-actions']({ row })))
        }
        children.push(h('div', { 'data-test': 'row' }, cells))
      }
      children.push(
        h('button', { 'data-test': 'do-sort', onClick: () => emit('sort', 'created_at', 'desc') }, 'sort'),
      )
      return h('div', { 'data-test': 'datatable' }, children)
    }
  },
}

// Pagination:两个按钮,分别 emit update:page / update:pageSize。
export const PaginationStub = {
  props: ['page', 'total', 'pageSize'],
  emits: ['update:page', 'update:pageSize'],
  setup(_: any, { emit }: any) {
    return () =>
      h('div', { 'data-test': 'pagination' }, [
        h('button', { 'data-test': 'next-page', onClick: () => emit('update:page', 2) }, 'next'),
        h('button', { 'data-test': 'change-size', onClick: () => emit('update:pageSize', 50) }, 'size'),
      ])
  },
}

// BaseDialog:show 为真时渲染默认插槽 + footer 插槽,并暴露 close 按钮。
export const BaseDialogStub = {
  props: ['show', 'title', 'width'],
  emits: ['close'],
  setup(props: any, { slots, emit }: any) {
    return () =>
      props.show
        ? h('div', { 'data-test': 'dialog' }, [
            h('div', { 'data-test': 'dialog-body' }, slots.default ? slots.default() : []),
            h('div', { 'data-test': 'dialog-footer' }, slots.footer ? slots.footer() : []),
            h('button', { 'data-test': 'dialog-close', onClick: () => emit('close') }, 'x'),
          ])
        : null
  },
}

export const IconStub = { props: ['name', 'size'], template: '<i />' }

// 一组通用 global 配置:i18n 直返 key,常用布局/表格/分页/对话框 stub 全装好。
export function pointsViewMountOptions(extraStubs: Record<string, unknown> = {}) {
  return {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: PaginationStub,
        BaseDialog: BaseDialogStub,
        Icon: IconStub,
        Teleport: true,
        ...extraStubs,
      },
    },
  }
}
