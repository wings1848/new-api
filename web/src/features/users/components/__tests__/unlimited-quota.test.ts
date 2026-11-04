import { describe, expect, it } from 'vitest'

import { transformUserToFormDefaults } from '../../lib/user-form'
import { getUserActionMessage } from '../../lib/user-actions'
import { userSchema } from '../../types'

const unlimitedUser = {
  id: 7,
  username: 'unlimited-user',
  display_name: 'Unlimited User',
  quota: 0,
  used_quota: 0,
  request_count: 0,
  group: 'default',
  status: 1,
  role: 1,
  unlimited_quota: true,
}

describe('user-level unlimited quota UI model', () => {
  it('keeps the unlimited flag parsed from the users API', () => {
    expect(userSchema.parse(unlimitedUser).unlimited_quota).toBe(true)
  })

  it('carries the unlimited flag into the edit form defaults', () => {
    const parsed = userSchema.parse(unlimitedUser)
    expect(transformUserToFormDefaults(parsed).unlimited_quota).toBe(true)
  })

  it('maps the new manage action to a success message', () => {
    expect(getUserActionMessage('set_unlimited_quota')).toBeTruthy()
  })
})
