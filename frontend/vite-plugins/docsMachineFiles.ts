import { createServer, type Plugin } from 'vite'
import { resolve } from 'path'

/**
 * 给 AI 读的文档文件：/llms.txt、/llms-full.txt、/docs/<章节 id>.md。
 * 内容和章节清单由 src/views/docs/docsMachine.ts 生成，新增章节自动出现。
 *
 * - 构建：把「带占位符的模板」写进 dist。生产由后端（backend/internal/web/docs_machine.go）
 *   在响应时按公开设置替换占位符，所以文件里不会有写死的域名。
 * - 开发：这里直接响应，用 docsMachine.ts 的 fillMachineText 替换，站点信息从后端的公开设置读。
 */

const MACHINE_MODULE = '/src/views/docs/docsMachine.ts'

interface MachineModule {
  buildMachineFiles: () => Record<string, string>
  fillMachineText: (template: string, ctx: Record<string, unknown>) => string
  ENDPOINT_QUERY_PARAM: string
}

export function isMachinePath(path: string): boolean {
  return path === 'llms.txt' || path === 'llms-full.txt' || /^docs\/[\w-]+\.md$/.test(path)
}

export function docsMachineFiles(options: { root: string; backendUrl: string }): Plugin {
  let builtFiles: Record<string, string> = {}

  return {
    name: 'docs-machine-files',

    // 构建时临时起一个不监听端口的 vite 服务，用它的模块加载（认得 ?raw 和 @ 别名）读出章节清单
    async buildStart() {
      if (this.meta.watchMode && Object.keys(builtFiles).length > 0) return
      const server = await createServer({
        configFile: false,
        root: options.root,
        logLevel: 'silent',
        appType: 'custom',
        server: { middlewareMode: true, hmr: false, watch: null },
        optimizeDeps: { noDiscovery: true, include: [] },
        resolve: { alias: { '@': resolve(options.root, 'src') } },
      })
      try {
        const mod = (await server.ssrLoadModule(MACHINE_MODULE)) as MachineModule
        builtFiles = mod.buildMachineFiles()
      } finally {
        await server.close()
      }
    },

    generateBundle() {
      for (const [fileName, source] of Object.entries(builtFiles)) {
        this.emitFile({ type: 'asset', fileName, source })
      }
    },

    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url || '/', 'http://localhost')
        const path = decodeURIComponent(url.pathname).replace(/^\/+/, '')
        if ((req.method !== 'GET' && req.method !== 'HEAD') || !isMachinePath(path)) return next()
        try {
          const mod = (await server.ssrLoadModule(MACHINE_MODULE)) as MachineModule
          const template = mod.buildMachineFiles()[path]
          if (template === undefined) return next()
          const settings = await fetchPublicSettings(options.backendUrl)
          const body = mod.fillMachineText(template, {
            site: settings.site_name || 'Sub2API',
            apiBaseUrl: settings.api_base_url || '',
            customEndpoints: settings.custom_endpoints || [],
            origin: `http://${req.headers.host || 'localhost'}`,
            requestedEndpoint: url.searchParams.get(mod.ENDPOINT_QUERY_PARAM) || '',
          })
          res.setHeader('Content-Type', path.endsWith('.md') ? 'text/markdown; charset=utf-8' : 'text/plain; charset=utf-8')
          res.setHeader('Cache-Control', 'no-cache')
          res.end(req.method === 'HEAD' ? undefined : body)
        } catch (e) {
          next(e)
        }
      })
    },
  }
}

interface PublicSettings {
  site_name?: string
  api_base_url?: string
  custom_endpoints?: Array<{ name?: string; endpoint?: string; description?: string }>
}

/** 后端没起来就用默认值，开发时照样能看到文件。 */
async function fetchPublicSettings(backendUrl: string): Promise<PublicSettings> {
  try {
    const response = await fetch(`${backendUrl}/api/v1/settings/public`, { signal: AbortSignal.timeout(1500) })
    if (response.ok) {
      const data = await response.json()
      if (data.code === 0 && data.data) return data.data as PublicSettings
    }
  } catch {
    // 回退到默认值
  }
  return {}
}
