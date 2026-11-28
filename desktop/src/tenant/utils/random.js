// Node and edge ids share one increasing sequence, matching the backend tools: a new id is the largest numeric id in the
// document plus one, written as a plain number ("2", "3", "1002" - no padding). Ids handed out in this batch but not yet
// in `nodes` / `edges` (one build() allocates a container plus its entry; branch sub-branch ids come the same way) are
// kept in a reservation set keyed by the nodes array, so a batch cannot hand out the same id twice before updateNodes()
// replaces the array.
const reservedIds = new WeakMap()

// Only plain non-negative integers take part in the sequence: older flows carry random ids and the default start node is
// "1", so anything else is simply ignored.
const highestSeq = (nodes, edges, reserved) => {
  let highest = 0
  const bump = (id) => {
    if (typeof id !== 'string') return
    const trimmed = id.trim()
    if (!/^\d+$/.test(trimmed)) return
    const n = Number(trimmed)
    if (n > highest) highest = n
  }
  nodes.forEach(node => {
    bump(node?.id)
    const children = node?.data?.config?.children || []
    children.forEach(child => bump(child?.id))
  })
  edges.forEach(edge => bump(edge?.id))
  reserved.forEach(bump)
  return highest
}

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

  generateUniqueId(nodes = [], edges = []) {
    let reserved = reservedIds.get(nodes)
    if (!reserved) {
      reserved = new Set()
      reservedIds.set(nodes, reserved)
    }
    const id = String(highestSeq(nodes, edges, reserved) + 1)
    reserved.add(id)
    return id
  }
}

export default random
