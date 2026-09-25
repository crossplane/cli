#!/usr/bin/env node

import { serve } from '@crossplane-org/function-sdk-typescript';
import { compose } from './function.js';

serve(compose, { name: '{{ .Name }}' });
