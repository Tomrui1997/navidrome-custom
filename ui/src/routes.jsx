import React from 'react'
import { Route } from 'react-router-dom'
import Personal from './personal/Personal'
import { Sync } from './syncing'

const routes = [
  <Route exact path="/personal" render={() => <Personal />} key={'personal'} />,
  <Route exact path="/sync" render={() => <Sync />} key={'sync'} />,
]

export default routes
