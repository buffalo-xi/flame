const random = {
  randomInt(min, max) {
    if (min === undefined) {
      return Math.floor(Math.random() * Number.MAX_SAFE_INTEGER)
    }
    if (max === undefined) {
      return Math.floor(Math.random() * min)
    }
    return Math.floor(Math.random() * (max - min + 1)) + min
  },

  randomFloat(min, max) {
    if (min === undefined) {
      return Math.random()
    }
    if (max === undefined) {
      return Math.random() * min
    }
    return Math.random() * (max - min) + min
  },

  randomString(length = 8) {
    const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
    let result = ''
    for (let i = 0; i < length; i++) {
      result += chars.charAt(Math.floor(Math.random() * chars.length))
    }
    return result
  },

  randomNumberString(length = 8) {
    const chars = '0123456789'
    let result = ''
    for (let i = 0; i < length; i++) {
      result += chars.charAt(Math.floor(Math.random() * chars.length))
    }
    return result
  },

  randomId(prefix = '') {
    const timestamp = Date.now().toString(36)
    const randomPart = this.randomString(6)
    return `${prefix}${timestamp}${randomPart}`
  },

  generateUniqueId(digits, nodes, edges) {
    while (true) {
      let id = this.randomNumberString(digits)
      // 校验节点自身ID + 节点内嵌的分支ID
      const nodeExists = nodes.some(node => {
        if (node.id === id) return true
        const children = node.data?.config?.children || []
        return children.some(child => child.id === id)
      })
      // 校验边ID
      const edgeExists = edges.some(edge => edge.id === id)
      if (!nodeExists && !edgeExists) {
        return id
      }
    }
  }
}

export default random