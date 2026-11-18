export function parseArguments(argsStr) {
  if (!argsStr) {
    return {}
  }
  try {
    return JSON.parse(argsStr)
  } catch {
    return {}
  }
}

export function stringifyArguments(args) {
  if (!args || typeof args !== 'object') {
    return ''
  }
  try {
    return JSON.stringify(args, null, 2)
  } catch {
    return ''
  }
}

export function getArgumentValue(args, key, defaultValue = null) {
  if (!args || typeof args !== 'object') {
    return defaultValue
  }
  return args[key] !== undefined ? args[key] : defaultValue
}

export function setArgumentValue(args, key, value) {
  if (!args || typeof args !== 'object') {
    return { [key]: value }
  }
  return {
    ...args,
    [key]: value
  }
}

export function removeArgument(args, key) {
  if (!args || typeof args !== 'object') {
    return {}
  }
  const newArgs = { ...args }
  delete newArgs[key]
  return newArgs
}

export function validateArguments(args, schema) {
  const errors = []
  if (!args) {
    return errors
  }
  Object.keys(schema).forEach(key => {
    const rule = schema[key]
    const value = args[key]
    
    if (rule.required && value === undefined) {
      errors.push(`${key} 是必填项`)
      return
    }
    
    if (value !== undefined) {
      if (rule.type && typeof value !== rule.type) {
        errors.push(`${key} 的类型必须是 ${rule.type}`)
      }
      
      if (rule.min !== undefined && typeof value === 'number' && value < rule.min) {
        errors.push(`${key} 不能小于 ${rule.min}`)
      }
      
      if (rule.max !== undefined && typeof value === 'number' && value > rule.max) {
        errors.push(`${key} 不能大于 ${rule.max}`)
      }
      
      if (rule.minLength !== undefined && typeof value === 'string' && value.length < rule.minLength) {
        errors.push(`${key} 的长度不能小于 ${rule.minLength}`)
      }
      
      if (rule.maxLength !== undefined && typeof value === 'string' && value.length > rule.maxLength) {
        errors.push(`${key} 的长度不能大于 ${rule.maxLength}`)
      }
      
      if (rule.pattern && typeof value === 'string' && !rule.pattern.test(value)) {
        errors.push(`${key} 格式不正确`)
      }
      
      if (rule.enum && !rule.enum.includes(value)) {
        errors.push(`${key} 必须是 ${rule.enum.join('、')} 中的一个`)
      }
    }
  })
  return errors
}

export function mergeArguments(target, source) {
  if (!target) {
    return source || {}
  }
  if (!source) {
    return target
  }
  return {
    ...target,
    ...source
  }
}

export function hasArguments(args) {
  return args && typeof args === 'object' && Object.keys(args).length > 0
}

export function getArgumentKeys(args) {
  if (!args || typeof args !== 'object') {
    return []
  }
  return Object.keys(args)
}

export function validateArgumentsSchema(data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) {
    return ['数据必须是对象类型']
  }
  if (data.type !== 'object') {
    return ['type 属性必须是 "object"']
  }
  if (!data.properties || typeof data.properties !== 'object' || Array.isArray(data.properties)) {
    return ['properties 属性必须存在且为对象类型']
  }
  
  const errors = []
  
  for (const key of Object.keys(data.properties)) {
    const propertySchema = data.properties[key]
    const propertyErrors = validatePropertySchema(propertySchema, `properties.${key}`)
    errors.push(...propertyErrors)
  }
  
  if (data.required) {
    if (!Array.isArray(data.required)) {
      errors.push('required 属性必须是数组类型')
    } else {
      for (const requiredKey of data.required) {
        if (!data.properties[requiredKey]) {
          errors.push(`required 中的 "${requiredKey}" 不存在于 properties 中`)
        }
      }
    }
  }
  
  return errors.length > 0 ? errors : []
}

function validatePropertySchema(schema, path) {
  const errors = []
  
  if (!schema || typeof schema !== 'object' || Array.isArray(schema)) {
    errors.push(`${path} 必须是对象类型`)
    return errors
  }
  
  if (!schema.type) {
    errors.push(`${path} 缺少 type 属性`)
    return errors
  }
  
  const validTypes = ['string', 'number', 'array', 'object']
  if (!validTypes.includes(schema.type)) {
    errors.push(`${path}.type 必须是 ${validTypes.join('、')} 中的一个`)
    return errors
  }
  
  if (schema.type === 'array') {
    if (!schema.items) {
      errors.push(`${path} 是数组类型时必须包含 items 属性`)
    } else {
      const itemErrors = validatePropertySchema(schema.items, `${path}.items`)
      errors.push(...itemErrors)
    }
  }  
  if (schema.type === 'object') {
    if (schema.properties) {
      if (typeof schema.properties !== 'object' || Array.isArray(schema.properties)) {
        errors.push(`${path}.properties 必须为对象类型`)
      } else {
        for (const key of Object.keys(schema.properties)) {
          const propertyErrors = validatePropertySchema(schema.properties[key], `${path}.properties.${key}`)
          errors.push(...propertyErrors)
        }
      }
    }    
    if (schema.required) {
      if (!Array.isArray(schema.required)) {
        errors.push(`${path}.required 必须是数组类型`)
      } else {
        for (const requiredKey of schema.required) {
          if (!schema.properties[requiredKey]) {
            errors.push(`${path}.required 中的 "${requiredKey}" 不存在于 ${path}.properties 中`)
          }
        }
      }
    }
  }  
  return errors
}

export function isValidArgumentsSchema(data) {
  const errors = validateArgumentsSchema(data)
  return {
    valid: errors.length === 0,
    errors: errors
  }
}

